// Command purge-uid is a one-shot CalDAV operator tool that removes
// a specific iCalendar UID from both sides (or one side) of a sync
// source, plus scrubs the synced_events tracking row for that UID.
//
// It exists because the sync engine cannot recover from pre-existing
// data corruption where two matching corrupted copies sit on both
// sides — normal two-way sync sees them as "in agreement" and will
// not touch them. The hotfixes in PRs #78/#79/#80/#82 stopped future
// damage but left already-corrupted events in place. purge-uid is
// the escape hatch for operators to surgically remove such events
// without having to wipe and re-create the source.
//
// Usage:
//
//	purge-uid --source-id=<id> --uid=<UID> [--side=both|source|dest] [--confirm]
//
// The tool is DRY-RUN by default. It reads the source row from the
// calbridgesync database, connects to each selected calendar on the
// chosen side(s) via the existing caldav.Client, searches for the
// given UID, and reports what it found. Nothing is deleted unless
// --confirm is passed.
//
// Limitations:
//   - Google OAuth source-side purging is not yet wired in this
//     tool (would need refresh-token flow replication). Use
//     --side=dest for Google sources, or remove the source via
//     the web UI.
//   - The tool uses the normal CalDAV PROPFIND/REPORT path to list
//     every event and filters client-side. On very large calendars
//     this is slower than a server-side calendar-query by UID, but
//     it works against any CalDAV server regardless of
//     calendar-query support.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/config"
	"github.com/macjediwizard/calbridgesync/internal/crypto"
	"github.com/macjediwizard/calbridgesync/internal/db"
)

func main() {
	// No log timestamps — this is an interactive tool, not a daemon.
	log.SetFlags(0)

	var (
		sourceID = flag.String("source-id", "", "source row ID to operate on (required)")
		uid      = flag.String("uid", "", "iCalendar UID to purge (required; case-sensitive, use the full UID as stored)")
		side     = flag.String("side", "both", "which side to purge from: source, dest, or both")
		confirm  = flag.Bool("confirm", false, "actually perform the delete (default is dry-run: read-only)")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "purge-uid — remove a specific iCalendar UID from a sync source\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  %s --source-id=<id> --uid=<UID> [--side=both|source|dest] [--confirm]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nThe tool is DRY-RUN by default. It will report what it would delete but\n")
		fmt.Fprintf(os.Stderr, "not touch anything unless --confirm is passed.\n")
	}
	flag.Parse()

	if *sourceID == "" || *uid == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *side != "both" && *side != "source" && *side != "dest" {
		log.Fatalf("invalid --side value %q (must be both, source, or dest)", *side)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	caldav.SetRequestTimeout(time.Duration(cfg.CalDAV.RequestTimeoutSecs) * time.Second)

	database, err := db.New(cfg.Database.Path)
	if err != nil {
		log.Fatalf("failed to open database at %s: %v", cfg.Database.Path, err)
	}
	defer func() { _ = database.Close() }()

	encryptor, err := crypto.NewEncryptor(cfg.Security.EncryptionKey)
	if err != nil {
		log.Fatalf("failed to initialize encryptor: %v", err)
	}

	source, err := database.GetSourceByID(*sourceID)
	if err != nil {
		log.Fatalf("failed to load source %q: %v", *sourceID, err)
	}

	// Always print the source identity prominently. This tool is
	// destructive if --confirm is set, and calbridgesync is a
	// multi-user instance — operators MUST see which user's data
	// they are about to touch before proceeding.
	fmt.Printf("=== purge-uid ===\n")
	fmt.Printf("Source ID:        %s\n", source.ID)
	fmt.Printf("Source name:      %s\n", source.Name)
	fmt.Printf("Owner (user_id):  %s\n", source.UserID)
	fmt.Printf("Source type:      %s\n", source.SourceType)
	fmt.Printf("Source URL:       %s\n", source.SourceURL)
	fmt.Printf("Dest URL:         %s\n", source.DestURL)
	fmt.Printf("Calendars sel'd:  %d\n", len(source.SelectedCalendars))
	fmt.Printf("UID to purge:     %s\n", *uid)
	fmt.Printf("Side:             %s\n", *side)
	fmt.Printf("Mode:             %s\n\n", modeLabel(*confirm))

	if source.SourceType == db.SourceTypeGoogle && (*side == "source" || *side == "both") {
		log.Fatalf("source-side purge is not yet supported for Google sources (OAuth flow not wired in this tool). " +
			"Re-run with --side=dest to purge from the destination only, or remove the source via the web UI.")
	}

	if len(source.SelectedCalendars) == 0 {
		log.Fatalf("source %q has no selected_calendars — nothing to purge from", source.Name)
	}

	ctx := context.Background()

	// Build the CalDAV clients we actually need for the requested
	// --side. We defer sourcing the refresh token / password until
	// here so a dry-run with --side=dest does not need source creds.
	var sourceClient, destClient *caldav.Client
	if *side == "both" || *side == "dest" {
		destPassword, err := encryptor.Decrypt(source.DestPassword)
		if err != nil {
			log.Fatalf("failed to decrypt dest password: %v", err)
		}
		destClient, err = caldav.NewClient(source.DestURL, source.DestUsername, destPassword)
		if err != nil {
			log.Fatalf("failed to create dest CalDAV client: %v", err)
		}
	}
	if *side == "both" || *side == "source" {
		sourcePassword, err := encryptor.Decrypt(source.SourcePassword)
		if err != nil {
			log.Fatalf("failed to decrypt source password: %v", err)
		}
		sourceClient, err = caldav.NewClient(source.SourceURL, source.SourceUsername, sourcePassword)
		if err != nil {
			log.Fatalf("failed to create source CalDAV client: %v", err)
		}
	}

	// The destination is a different server with its own collection
	// layout, so the source calendar path from selected_calendars is
	// meaningless there. Discover the destination calendar exactly as
	// the sync engine does so we search where events were written.
	var destCalendarPath string
	if destClient != nil {
		destCalendarPath = discoverDestCalendarPath(ctx, destClient, source.DestURL)
		fmt.Printf("Dest calendar:    %s\n\n", destCalendarPath)
	}

	var totalFound, totalDeleted, totalErrors int

	for _, calCfg := range source.SelectedCalendars {
		fmt.Printf("Calendar: %s\n", calCfg.Path)

		var targets []sideTarget
		if destClient != nil {
			targets = append(targets, sideTarget{label: "dest", client: destClient, calendarPath: destCalendarPath})
		}
		if sourceClient != nil {
			targets = append(targets, sideTarget{label: "source", client: sourceClient, calendarPath: calCfg.Path})
		}

		calendarPath := calCfg.Path
		found, del, errs := purgeCalendar(ctx, targets, *uid, *confirm, func() error {
			return database.DeleteSyncedEvent(source.ID, calendarPath, *uid)
		})
		totalFound += found
		totalDeleted += del
		totalErrors += errs
		if !*confirm {
			fmt.Printf("  synced_events: would scrub row if all deletes succeed (source_id=%s, calendar=%s, uid=%s)\n", source.ID, calCfg.Path, *uid)
		}
		fmt.Println()
	}

	fmt.Printf("=== Summary ===\n")
	fmt.Printf("Found (across calendars/sides): %d\n", totalFound)
	if *confirm {
		fmt.Printf("Deleted:                        %d\n", totalDeleted)
		fmt.Printf("Errors:                         %d\n", totalErrors)
		if totalErrors > 0 {
			os.Exit(1)
		}
	} else {
		fmt.Printf("Mode: DRY-RUN — re-run with --confirm to actually delete\n")
	}
}

// eventClient is the subset of *caldav.Client used to search and
// delete events. It exists so purgeCalendar can be unit tested.
type eventClient interface {
	GetEvents(ctx context.Context, calendarPath string, collector *caldav.MalformedEventCollector) ([]caldav.Event, error)
	DeleteEvent(ctx context.Context, eventPath string) error
}

// calendarFinder is the subset of *caldav.Client used to discover the
// destination calendar path.
type calendarFinder interface {
	FindCalendars(ctx context.Context) ([]caldav.Calendar, error)
	FindCalendarsGoogle(ctx context.Context) ([]caldav.Calendar, error)
	GetCalendarPath() string
}

// discoverDestCalendarPath returns the destination calendar path using
// the same rules as SyncEngine.syncCalendar / fullSync: Google URLs use
// FindCalendarsGoogle, everything else FindCalendars; the first
// discovered calendar wins, and discovery failure or an empty result
// falls back to the path of the configured destination URL.
func discoverDestCalendarPath(ctx context.Context, client calendarFinder, destURL string) string {
	var (
		cals []caldav.Calendar
		err  error
	)
	if caldav.IsGoogleURL(destURL) {
		cals, err = client.FindCalendarsGoogle(ctx)
	} else {
		cals, err = client.FindCalendars(ctx)
	}
	if err != nil {
		log.Printf("WARNING: destination calendar discovery failed, falling back to URL path: %v", err)
		return client.GetCalendarPath()
	}
	if len(cals) == 0 {
		return client.GetCalendarPath()
	}
	if len(cals) > 1 {
		log.Printf("WARNING: multiple destination calendars found, using first one (same as sync engine): %s", cals[0].Path)
	}
	return cals[0].Path
}

// sideTarget is one side (source or dest) of one calendar to purge.
type sideTarget struct {
	label        string
	client       eventClient
	calendarPath string
}

// purgeCalendar searches each target for targetUID and, when confirm is
// set, deletes every match. The synced_events scrub runs only when
// confirm is set AND every target finished with zero search/delete
// errors: if a remote object survived, its tracking row must survive
// too, or the next sync treats the leftover as never-seen (PR #90).
// Returns aggregate (found, deleted, errors) counts.
func purgeCalendar(ctx context.Context, targets []sideTarget, targetUID string, confirm bool, scrub func() error) (int, int, int) {
	var found, deleted, errs int
	for _, t := range targets {
		f, d, e := handleSide(ctx, t.label, t.client, t.calendarPath, targetUID, confirm)
		found += f
		deleted += d
		errs += e
	}
	if !confirm {
		return found, deleted, errs
	}
	if errs > 0 {
		fmt.Printf("  synced_events: scrub SKIPPED (%d error(s) above; tracking row kept so it matches the surviving remote state)\n", errs)
		return found, deleted, errs
	}
	if err := scrub(); err != nil {
		fmt.Printf("  synced_events: scrub FAILED: %v\n", err)
		return found, deleted, errs + 1
	}
	fmt.Printf("  synced_events: scrubbed\n")
	return found, deleted, errs
}

// handleSide searches a single CalDAV calendar for the target UID
// and optionally deletes every match. Returns (found, deleted, errors)
// counts so the top-level summary can aggregate across calendars/sides.
func handleSide(ctx context.Context, label string, client eventClient, calendarPath, targetUID string, confirm bool) (int, int, int) {
	events, err := client.GetEvents(ctx, calendarPath, caldav.NewMalformedEventCollector())
	if err != nil {
		fmt.Printf("  %s: ERROR searching calendar %s: %v\n", label, calendarPath, err)
		return 0, 0, 1
	}
	paths := findUIDInEvents(events, targetUID)
	if len(paths) == 0 {
		fmt.Printf("  %s: not present in %s\n", label, calendarPath)
		return 0, 0, 0
	}
	var deleted, errs int
	for _, p := range paths {
		fmt.Printf("  %s: FOUND at %s\n", label, p)
		if !confirm {
			fmt.Printf("  %s: would DELETE (dry-run)\n", label)
			continue
		}
		if err := client.DeleteEvent(ctx, p); err != nil {
			fmt.Printf("  %s: DELETE failed: %v\n", label, err)
			errs++
			continue
		}
		fmt.Printf("  %s: DELETED\n", label)
		deleted++
	}
	return len(paths), deleted, errs
}

// findUIDInEvents scans a slice of CalDAV events for targetUID and
// returns the path of every matching event (nil if none).
//
// Two passes, results de-duplicated, parsed matches first:
//  1. The parsed Event.UID field equals targetUID — normal case.
//  2. The raw Event.Data, after RFC 5545 line unfolding, contains a
//     line that is exactly "UID:<target>" — catches the zombie-recovery
//     case where the parser dropped or mangled the UID but the raw
//     VEVENT still carries it. The whole-line match means purging
//     "abc" never selects "abcd". Property parameters like
//     "UID;X-PARAM=...:" are non-standard and not handled here.
//
// Every match is returned because a calendar can hold several objects
// for one UID (the corrupted-duplicate case this tool exists for).
func findUIDInEvents(events []caldav.Event, targetUID string) []string {
	var paths []string
	seen := make(map[string]bool)
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for i := range events {
		if events[i].UID == targetUID {
			add(events[i].Path)
		}
	}
	needle := "UID:" + targetUID
	for i := range events {
		if hasLine(events[i].Data, needle) {
			add(events[i].Path)
		}
	}
	return paths
}

// icalUnfolder removes RFC 5545 section 3.1 line folds (CRLF or LF
// followed by a single space or tab).
var icalUnfolder = strings.NewReplacer("\r\n ", "", "\r\n\t", "", "\n ", "", "\n\t", "")

// hasLine reports whether the unfolded iCalendar data contains a line
// exactly equal to want (ignoring a trailing CR).
func hasLine(data, want string) bool {
	for _, line := range strings.Split(icalUnfolder.Replace(data), "\n") {
		if strings.TrimSuffix(line, "\r") == want {
			return true
		}
	}
	return false
}

func modeLabel(confirm bool) string {
	if confirm {
		return "CONFIRM (will delete)"
	}
	return "DRY-RUN (read-only)"
}
