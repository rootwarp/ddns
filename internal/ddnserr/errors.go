package ddnserr

import "errors"

// Sentinel errors used by higher-level packages to drive exit-code mapping
// in cmd/ddns. Callers must wrap these (`fmt.Errorf("...: %w", ErrX)`) rather
// than string-comparing so errors.Is continues to work across wrappings.
var (
	// ErrNoQuorum — resolver could not reach agreement across echo services.
	ErrNoQuorum = errors.New("resolver: no quorum among echo services")
	// ErrNotFound — DNS record not found at the provider.
	ErrNotFound = errors.New("provider: record not found")
	// ErrTransient — provider returned a retriable error (5xx, network flake).
	ErrTransient = errors.New("provider: transient error, will retry")
	// ErrConfig — config validation failed.
	ErrConfig = errors.New("config: validation failed")
	// ErrAuth — provider authentication failed (bad ADC, missing scope).
	ErrAuth = errors.New("provider: authentication failed")
)
