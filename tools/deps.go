//go:build tools

// Package tools blank-imports the runtime dependencies that future phases
// will consume, so `go mod tidy` retains their pins while the phases they
// belong to are still being written. Once a dependency is used by real code,
// its entry here can be removed.
package tools

import (
	_ "golang.org/x/oauth2"
	_ "google.golang.org/api/dns/v1"
)
