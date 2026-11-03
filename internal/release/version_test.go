package release

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for _, c := range []struct {
		text string
		ok   bool
	}{
		{"0.12.0", true},
		{"1.2.3-rc.1", true},
		{DevVersion, true},
		{"", false},
		{"v0.12.0", false},
		{"0.12", false},
		{"01.2.3", false},
		{"0.12.0+build", false},
		{"0.12.0-rc.1+build", false},
		{"0.12.0+", false},
	} {
		v, err := ParseVersion(c.text)
		if c.ok != (err == nil) {
			t.Errorf("ParseVersion(%q) error = %v, want success: %v", c.text, err, c.ok)
		}
		if !c.ok && !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("ParseVersion(%q) error = %v, want ErrInvalidVersion", c.text, err)
		}
		if c.ok && c.text == "1.2.3-rc.1" && (v.Major != 1 || v.Minor != 2 || v.Patch != 3 || len(v.Pre) != 2) {
			t.Errorf("ParseVersion(%q) = %+v", c.text, v)
		}
	}
}
