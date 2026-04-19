package ddnserr

import (
	"errors"
	"testing"
)

// All five sentinels must be distinct error values so errors.Is can tell
// them apart; the exit-code mapping depends on this.
func TestSentinels_AreDistinct(t *testing.T) {
	sentinels := map[string]error{
		"ErrNoQuorum":  ErrNoQuorum,
		"ErrNotFound":  ErrNotFound,
		"ErrTransient": ErrTransient,
		"ErrConfig":    ErrConfig,
		"ErrAuth":      ErrAuth,
	}
	for name, e := range sentinels {
		if e == nil {
			t.Fatalf("%s is nil", name)
		}
		if e.Error() == "" {
			t.Fatalf("%s.Error() is empty", name)
		}
	}
	// Pairwise: each sentinel must only match itself under errors.Is.
	for outerName, outer := range sentinels {
		for innerName, inner := range sentinels {
			if outerName == innerName {
				if !errors.Is(outer, inner) {
					t.Errorf("errors.Is(%s, %s) = false, want true (self-identity)", outerName, innerName)
				}
				continue
			}
			if errors.Is(outer, inner) {
				t.Errorf("errors.Is(%s, %s) = true, want false (distinctness)", outerName, innerName)
			}
		}
	}
}
