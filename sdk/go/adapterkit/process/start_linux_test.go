package process

import (
	"strings"
	"testing"
)

const (
	underManager = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/tmux-spawn-1.scope\n"
	inSession    = "0::/user.slice/user-1000.slice/session-3.scope\n"
)

func TestUnderUserManager(t *testing.T) {
	for _, tc := range []struct {
		name, cgroup string
		uid          int
		want         bool
	}{
		{"under the manager", underManager, 1000, true},
		{"login session", inSession, 1000, false},
		{"manager of another user", underManager, 1001, false},
		{"v1 hierarchy", "12:cpu:/\n1:name=systemd:/user.slice/user-1000.slice/user@1000.service/app.slice/x.scope\n", 1000, true},
		{"a system service", "0::/system.slice/sidera.service\n", 1000, false},
		{"manager itself", "0::/user.slice/user-1000.slice/user@1000.service\n", 1000, false},
	} {
		if got := underUserManager(tc.cgroup, tc.uid); got != tc.want {
			t.Errorf("%s: underUserManager = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// With a usable user manager a scope is used when linger keeps it alive or the
// daemon is under it; a daemon in a login session without linger is better off
// with a session of its own, which survives the logout.
func TestChooseScope(t *testing.T) {
	for _, tc := range []struct {
		linger, under bool
		scope         bool
		caveat        string
	}{
		{true, true, true, "linger is enabled"},
		{true, false, true, "linger is enabled"},
		{false, true, true, "ends with the user manager at the last logout"},
		{false, false, false, "not in a systemd user scope"},
	} {
		run, d := chooseScope("/usr/bin/systemd-run", tc.linger, tc.under)
		if (run != "") != tc.scope || !d.OutlivesDaemon || !strings.Contains(d.Caveat, tc.caveat) {
			t.Errorf("chooseScope(linger %v, under %v) = %q, %+v, want scope %v and %q in the caveat", tc.linger, tc.under, run, d, tc.scope, tc.caveat)
		}
	}
}

func TestUnitNameIsAValidUniqueUnit(t *testing.T) {
	a, b := unitName("/opt/my prog/claude.exe"), unitName("/opt/my prog/claude.exe")
	if a == b {
		t.Fatalf("two unit names are both %q", a)
	}
	if !strings.HasPrefix(a, "sidera-claude-") || !strings.HasSuffix(a, ".scope") || len(a) != len("sidera-claude-")+16+len(".scope") {
		t.Fatalf("unit name %q", a)
	}
	if n := unitName("/x/we ird:é"); !strings.HasPrefix(n, "sidera-we_ird__-") {
		t.Fatalf("unit name %q keeps characters a unit name may not hold", n)
	}
}
