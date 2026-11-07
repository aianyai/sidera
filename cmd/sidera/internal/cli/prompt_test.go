package cli

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

// The two ways to ask differ in what Enter means and nowhere else; the end of the input
// is a no for both, so that no script and no dropped terminal accepts a change.
func TestAsk(t *testing.T) {
	for _, tc := range []struct {
		input                         string
		wantDefaultNo, wantDefaultYes bool
	}{
		{"y\n", true, true},
		{"YES\n", true, true},
		{" Yes \n", true, true},
		{"n\n", false, false},
		{"No\n", false, false},
		{"\n", false, true},
		{"  \n", false, true},
		{"sure\n", false, false},
		{"yy\n", false, false},
		{"", false, false},
		{"y", true, true}, // the last line has no newline, and is an answer
		{"  ", false, false},
	} {
		for _, d := range []struct {
			name string
			ask  func(*bufio.Reader, *bytes.Buffer) (bool, error)
			want bool
			text string
		}{
			{"default no", func(r *bufio.Reader, o *bytes.Buffer) (bool, error) { return askDefaultNo(o, r, "go?") }, tc.wantDefaultNo, "go? [y/N] "},
			{"default yes", func(r *bufio.Reader, o *bytes.Buffer) (bool, error) { return askDefaultYes(o, r, "go?") }, tc.wantDefaultYes, "go? [Y/n] "},
		} {
			var out bytes.Buffer
			got, err := d.ask(bufio.NewReader(strings.NewReader(tc.input)), &out)
			if err != nil || got != d.want {
				t.Errorf("%s, input %q: %v, %v; want %v", d.name, tc.input, got, err, d.want)
			}
			if out.String() != d.text {
				t.Errorf("%s, input %q: asked %q, want %q", d.name, tc.input, out.String(), d.text)
			}
		}
	}
}

// Questions asked in a row share one reader and each takes one line.
func TestAskTakesOneLineEach(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("\nn\n\n"))
	var out bytes.Buffer
	var got []bool
	for range 3 {
		ok, err := askDefaultYes(&out, r, "go?")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ok)
	}
	if want := []bool{true, false, true}; got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("answers = %v, want %v", got, want)
	}
}
