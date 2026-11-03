package process

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
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

// systemd-run reaches the user manager through the adapter's own variables, which
// a start with an environment of its own may leave out; such a start must not be
// put in a scope, where it would exit 1 after Start returned.
func TestPlaceFallsBackWhenTheEnvironmentLacksTheUserManager(t *testing.T) {
	adapter := []string{"PATH=/bin", "XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"}
	hostScope := Detachment{OutlivesDaemon: true, Caveat: "runs in a scope"}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		env     []string
		adapter []string
		scope   bool
		missing string // named in the caveat of a start without a scope
	}{
		{"the adapter's environment", nil, adapter, true, ""},
		{"all kept", []string{"A=1", "XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=x"}, adapter, true, ""},
		{"no runtime dir", []string{"DBUS_SESSION_BUS_ADDRESS=x"}, adapter, false, "XDG_RUNTIME_DIR"},
		{"no bus address", []string{"XDG_RUNTIME_DIR=/run/user/1000"}, adapter, false, "DBUS_SESSION_BUS_ADDRESS"},
		{"empty", []string{}, adapter, false, "XDG_RUNTIME_DIR"},
		{"empty value", []string{"XDG_RUNTIME_DIR=", "DBUS_SESSION_BUS_ADDRESS=x"}, adapter, false, "XDG_RUNTIME_DIR"},
		{"the adapter has none either", []string{"A=1"}, []string{"PATH=/bin"}, true, ""},
	} {
		argv, container, d, err := placeWith("/usr/bin/systemd-run", hostScope, tc.adapter, Spec{Argv: []string{exe}, Env: tc.env})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if tc.scope {
			if container.Kind != adapterproto.ContainerSystemdUnit || argv[0] != "/usr/bin/systemd-run" || d != hostScope {
				t.Errorf("%s: placed as %q, %+v, %+v, want a scope", tc.name, argv, container, d)
			}
			continue
		}
		if container.Kind != adapterproto.ContainerProcessGroup || len(argv) != 1 || argv[0] != exe ||
			!d.OutlivesDaemon || !strings.Contains(d.Caveat, tc.missing) {
			t.Errorf("%s: placed as %q, %+v, %+v, want a session only naming %s", tc.name, argv, container, d, tc.missing)
		}
	}
}

// A path that is not a regular executable file fails the start in the way a start
// without a scope would, before systemd-run is involved.
func TestCheckExecutable(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, tc := range []struct {
		name, path string
		ok         bool
	}{
		{"executable file", write("run", 0o755), true},
		{"file that is not executable", write("data", 0o644), false},
		{"directory", dir, false},
		{"missing", filepath.Join(dir, "missing"), false},
	} {
		err := checkExecutable(tc.path)
		if (err == nil) != tc.ok {
			t.Errorf("%s: checkExecutable = %v, want ok %v", tc.name, err, tc.ok)
		}
		var pe *os.PathError
		if err != nil && (!errors.As(err, &pe) || pe.Op != "fork/exec") {
			t.Errorf("%s: %v is not a fork/exec path error", tc.name, err)
		}
	}
}
