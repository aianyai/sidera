package verify

import (
	"errors"
	"testing"
)

// The floor rule is tried on Releases made here: Verify would need a signed
// fixture for every floor, and the signature is not what this test is about.
func TestAdmits(t *testing.T) {
	tests := []struct {
		floor, installed string
		want             error
	}{
		{"0.8.0", "0.8.0", nil},
		{"0.8.0", "0.8.1", nil},
		{"0.8.0", "0.12.0", nil},
		{"0.8.0", "1.0.0", nil},
		{"0.8.0", "0.8.1-rc.1", nil},
		{"0.8.0", "0.7.9", ErrNotAllowed},
		{"0.8.0", "0.0.0", ErrNotAllowed},
		{"0.8.0", "0.0.0-dev", ErrNotAllowed},
		// A pre-release is below its release by SemVer precedence.
		{"0.8.0", "0.8.0-rc.1", ErrNotAllowed},
		{"0.8.0", "0.8.0-0", ErrNotAllowed},
		{"0.10.0", "0.9.9", ErrNotAllowed},
		{"0.10.0", "0.10.0", nil},
		{"1.0.0", "0.99.99", ErrNotAllowed},
		// Text that is not an installed release version is refused, not guessed at.
		{"0.8.0", "", ErrInvalidVersion},
		{"0.8.0", "v0.9.0", ErrInvalidVersion},
		{"0.8.0", "0.9.0+build", ErrInvalidVersion},
		{"0.8.0", "0.9", ErrInvalidVersion},
	}
	for _, tt := range tests {
		t.Run(tt.floor+"/"+tt.installed, func(t *testing.T) {
			err := Release{version: "0.12.0", floor: tt.floor}.Admits(tt.installed)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Admits(%q) with floor %q = %v, want %v", tt.installed, tt.floor, err, tt.want)
			}
			if errors.Is(err, ErrNotTrusted) {
				t.Errorf("a floor refusal must not read as untrustworthy: %v", err)
			}
		})
	}
}
