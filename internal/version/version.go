package version

import "fmt"

// Version, Commit, and BuildDate are populated at build time via
// `-ldflags "-X github.com/rootwarp/ddns/internal/version.<Name>=..."`.
// They are package-level vars (not consts) because -X only works on vars.
//
// Defaults correspond to a `go run`/`go install` build with no ldflags.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// String returns a human-readable one-liner for `ddns version`.
func String() string {
	return fmt.Sprintf("ddns %s (commit %s, built %s)", Version, Commit, BuildDate)
}
