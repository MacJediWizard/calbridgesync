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

	var totalFound, totalDeleted, totalErrors int

	for _, calCfg := range source.SelectedCalendars {
		fmt.Printf("Calendar: %s\n", calCfg.Path)

		if *side == "both" || *side == "dest" {
			found, del, errs := handleSide(ctx, "dest", destClient, calCfg.Path, *uid, *confirm)
			totalFound += found
			totalDeleted += del
			totalErrors += errs
		}

		if *side == "both" || *side == "source" {
			found, del, errs := handleSide(ctx, "source", sourceClient, calCfg.Path, *uid, *confirm)
			totalFound += found
			totalDeleted += del
			totalErrors += errs
		}

		// Scrub the synced_events tracking row for this calendar +
		// UID regardless of whether we found/deleted on each side.
		// If the UID was already gone from both sides but the row
		// still existed, this stops the sync engine from treating
		// future server-side additions as "previously synced" and
		// incorrectly planning a deletion.
		if *confirm {
			if err := database.DeleteSyncedEvent(source.ID, calCfg.Path, *uid); err != nil {
				fmt.Printf("  synced_events: scrub FAILED: %v\n", err)
				totalErrors++
			} else {
				fmt.Printf("  synced_events: scrubbed\n")
			}
		} else {
			fmt.Printf("  synced_events: would scrub row (source_id=%s, calendar=%s, uid=%s)\n", source.ID, calCfg.Path, *uid)
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

// handleSide searches a single CalDAV calendar for the target UID
// and optionally deletes it. Returns (found, deleted, errors) counts
// so the top-level summary can aggregate across calendars/sides.
func handleSide(ctx context.Context, label string, client *caldav.Client, calendarPath, targetUID string, confirm bool) (int, int, int) {
	foundPath, err := findUIDInCalendar(ctx, client, calendarPath, targetUID)
	if err != nil {
		fmt.Printf("  %s: ERROR searching calendar: %v\n", label, err)
		return 0, 0, 1
	}
	if foundPath == "" {
		fmt.Printf("  %s: not present\n", label)
		return 0, 0, 0
	}
	fmt.Printf("  %s: FOUND at %s\n", label, foundPath)
	if !confirm {
		fmt.Printf("  %s: would DELETE (dry-run)\n", label)
		return 1, 0, 0
	}
	if err := client.DeleteEvent(ctx, foundPath); err != nil {
		fmt.Printf("  %s: DELETE failed: %v\n", label, err)
		return 1, 0, 1
	}
	fmt.Printf("  %s: DELETED\n", label)
	return 1, 1, 0
}

// findUIDInCalendar lists every event in a calendar and returns the
// path of the one matching targetUID, or empty string if not found.
//
// Thin wrapper around findUIDInEvents that does the CalDAV I/O. The
// pure matching logic lives in findUIDInEvents so it can be unit
// tested without mocking a CalDAV server.
//
// Returns (path, err). err is non-nil only on transport failure; a
// missing UID is reported as ("", nil).
func findUIDInCalendar(ctx context.Context, client *caldav.Client, calendarPath, targetUID string) (string, error) {
	collector := caldav.NewMalformedEventCollector()
	events, err := client.GetEvents(ctx, calendarPath, collector)
	if err != nil {
		return "", err
	}
	return findUIDInEvents(events, targetUID), nil
}

// findUIDInEvents scans a slice of CalDAV events for targetUID and
// returns the event path, or empty string if not found.
//
// It checks two things for each event in priority order:
//  1. The parsed Event.UID field — normal case. Returns on first hit.
//  2. A raw substring match against Event.Data for "UID:<target>" —
//     catches the pathological case where the iCalendar parser
//     dropped or mangled the UID property but the raw VEVENT block
//     still carries it. This matters for zombie-recovery scenarios
//     where the event is partially corrupted and the parser
//     returned an empty or wrong UID.
//
// If the parsed-UID pass and the raw-data pass would both match but
// at different paths, the parsed-UID pass wins (it's the
// authoritative match). This keeps the behavior deterministic when
// both the live form and a corrupted form of the same UID happen to
// coexist in one calendar.
func findUIDInEvents(events []caldav.Event, targetUID string) string {
	// Pass 1: parsed UID match.
	for i := range events {
		if events[i].UID == targetUID {
			return events[i].Path
		}
	}
	// Pass 2: raw substring fallback. "UID:<value>" is the standard
	// line format in iCalendar (RFC 5545 §3.8.4.7). We don't try to
	// handle property parameters like "UID;X-PARAM=...:" here —
	// that's non-standard and the parsed UID should catch it.
	needle := "UID:" + targetUID
	for i := range events {
		if strings.Contains(events[i].Data, needle) {
			return events[i].Path
		}
	}
	return ""
}

func modeLabel(confirm bool) string {
	if confirm {
		return "CONFIRM (will delete)"
	}
	return "DRY-RUN (read-only)"
}
