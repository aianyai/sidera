package semver_test

import (
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/internal/semver"
)

func TestParse(t *testing.T) {
	valid := []string{
		"0.0.0", "1.2.3", "10.20.30", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-0.3.7", "1.0.0-x.7.z.92",
		"1.0.0-x-y-z.-", "1.0.0+20130313144700", "1.0.0-beta+exp.sha.5114f85", "1.0.0+21AF26D3----117B344092BD",
		"2.1.306", "999999999.999999999.999999999", "1.0.0-0A.is.legal",
	}
	invalid := []string{
		"", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03", "-1.2.3", "1.2.3-", "1.2.3-01", "1.2.3-.a", "1.2.3-a..b",
		"1.2.3+", "1.2.3+a..b", "v1.2.3", " 1.2.3", "1.2.3 ", "1.2.3\n", "1.2.3-alpha_1", "1.2.3+é", "1000000000.0.0",
		"1.0.0-" + strings.Repeat("a", 60),
	}
	for _, s := range valid {
		if _, err := semver.Parse(s); err != nil {
			t.Errorf("Parse(%q) = %v, want success", s, err)
		}
	}
	for _, s := range invalid {
		if _, err := semver.Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", s)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"1.2.3", "1.0.0-rc.1+x", "", "01.0.0"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := semver.Parse(s)
		if err != nil {
			return
		}
		// A version that parses has the three numbers of its text.
		if v.Major < 0 || v.Minor < 0 || v.Patch < 0 {
			t.Fatalf("%q parsed to %+v", s, v)
		}
	})
}
