package semver_test

import (
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/semver"
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

// TestCompare covers the precedence rules of Semantic Versioning 2.0.0, section 11.
func TestCompare(t *testing.T) {
	// Each chain is in strictly increasing precedence, as the specification
	// lists them; every pair of one chain is checked, in both orders.
	chains := [][]string{
		// Section 11.2: compared numerically, left to right.
		{"1.0.0", "2.0.0", "2.1.0", "2.1.1"},
		{"1.9.0", "1.10.0", "1.11.0"},
		// Section 11.3 and 11.4: a pre-release precedes its release, and
		// identifiers are compared from left to right.
		{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0"},
		// Numeric identifiers are compared as numbers, however long, and
		// precede alphanumeric ones; alphanumeric ones compare in ASCII order.
		{"1.0.0-1", "1.0.0-2", "1.0.0-10", "1.0.0-99999999999999999999", "1.0.0-100000000000000000000", "1.0.0-0a", "1.0.0-A", "1.0.0-a", "1.0.0-a-"},
		// A larger set of fields has higher precedence when all before are equal.
		{"1.0.0-a", "1.0.0-a.0", "1.0.0-a.0.0"},
	}
	for _, chain := range chains {
		for i := range chain {
			for j := range chain {
				a, b := mustParse(t, chain[i]), mustParse(t, chain[j])
				want := 0
				if i < j {
					want = -1
				} else if i > j {
					want = 1
				}
				if got := semver.Compare(a, b); got != want {
					t.Errorf("Compare(%q, %q) = %d, want %d", chain[i], chain[j], got, want)
				}
			}
		}
	}

	// Section 10: build metadata does not take part in precedence.
	if got := semver.Compare(mustParse(t, "1.0.0+a"), mustParse(t, "1.0.0+b")); got != 0 {
		t.Errorf("Compare of versions that differ only in build metadata = %d, want 0", got)
	}
	if got := semver.Compare(mustParse(t, "1.0.0-rc.1+a"), mustParse(t, "1.0.0-rc.1")); got != 0 {
		t.Errorf("Compare of a version and the same with build metadata = %d, want 0", got)
	}
}

func mustParse(t *testing.T, s string) semver.Version {
	t.Helper()
	v, err := semver.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q) = %v", s, err)
	}
	return v
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
