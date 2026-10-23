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

// TestCompare uses the precedence examples of the Semantic Versioning 2.0.0
// specification, item 11, written out in increasing order.
func TestCompare(t *testing.T) {
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
		"2.0.0", "2.1.0", "2.1.1", "2.1.10", "10.0.0",
	}
	for i, a := range ordered {
		for j, b := range ordered {
			va, _ := semver.Parse(a)
			vb, _ := semver.Parse(b)
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := semver.Compare(va, vb); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestCompareIgnoresBuildMetadata(t *testing.T) {
	a, _ := semver.Parse("1.0.0+build.1")
	b, _ := semver.Parse("1.0.0+build.2")
	if semver.Compare(a, b) != 0 {
		t.Fatal("build metadata changed precedence")
	}
}

func TestCompareLongNumericIdentifiers(t *testing.T) {
	a, _ := semver.Parse("1.0.0-99999999999999999999")
	b, _ := semver.Parse("1.0.0-100000000000000000000")
	if semver.Compare(a, b) != -1 {
		t.Fatal("long numeric pre-release identifiers compared wrongly")
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
		if semver.Compare(v, v) != 0 {
			t.Fatalf("%q is not equal to itself", s)
		}
	})
}
