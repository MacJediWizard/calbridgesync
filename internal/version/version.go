// Package version holds the build version string. The variable is
// set at compile time via ldflags in the Dockerfile:
//
//	go build -ldflags="-X github.com/macjediwizard/calbridgesync/internal/version.Version=0.0.66"
//
// When running from source without ldflags, Version defaults to "dev".
package version

// Version is the semantic version of the calbridgesync binary,
// injected at build time. Used by the /api/version endpoint and
// displayed in the web UI footer.
var Version = "dev"
