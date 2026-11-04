// Package version carries build-time information injected via -ldflags.
package version

import "fmt"

// Version, Commit and Date are set by ldflags at build time.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns a human-readable build identifier.
func String() string {
	return fmt.Sprintf("%s (%s, %s)", Version, Commit, Date)
}
