//go:build tools

// Package tools blank-imports the runtime dependencies that the v1 implementation
// will consume, so `go mod tidy` retains their pins across the phase-0 scaffold
// (which has no functional code yet). Phase 1 replaces these with real imports
// in the implementation packages, and this file can then be deleted.
package tools

import (
	_ "github.com/urfave/cli/v3"
	_ "golang.org/x/oauth2"
	_ "golang.org/x/term"
	_ "google.golang.org/api/dns/v1"
	_ "gopkg.in/yaml.v3"
)
