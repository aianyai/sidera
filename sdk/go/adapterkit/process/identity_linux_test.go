package process

import "testing"

// The command name of /proc/<pid>/stat is free text in parentheses; the fields are
// counted from its last ')'.
func TestParseStatCountsFromTheLastParenthesis(t *testing.T) {
	const tail = " S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242 20 21"
	for _, tc := range []struct {
		name, stat string
	}{
		{"plain", "123 (bash)" + tail},
		{"spaces", "123 (my prog 1 2 3)" + tail},
		{"parentheses", "123 (a) (b) c)" + tail},
		{"empty name", "123 ()" + tail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, ticks, err := parseStat([]byte(tc.stat + "\n"))
			if err != nil || state != 'S' || ticks != 424242 {
				t.Fatalf("parseStat = %q, %d, %v, want 'S', 424242", state, ticks, err)
			}
		})
	}
	for _, bad := range []string{"", "123 bash S 1", "123 (x) S 1 2", "123 (x) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 late 20"} {
		if _, _, err := parseStat([]byte(bad)); err == nil {
			t.Errorf("parseStat(%q) did not fail", bad)
		}
	}
}
