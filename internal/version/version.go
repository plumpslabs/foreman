package version

import "fmt"

var (
	// Version is the current semver release of Foreman.
	Version = "v0.1.0"
	// Commit is the git commit hash, populated at build time via -ldflags.
	Commit = "none"
	// Date is the build timestamp, populated at build time via -ldflags.
	Date = "unknown"
)

// Tagline is the CLI's one-line motto.
const Tagline = "local-first agent workspace"

// Info returns the formatted version information for CLI output.
func Info() string {
	return fmt.Sprintf("Foreman %s", Version)
}
