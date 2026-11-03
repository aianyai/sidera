package process

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"golang.org/x/sys/unix"
)

// How a process is detached on Linux is decided once per process, by what the host
// offers, and every Start follows it.
//
// A process always gets a session of its own (Setsid): a hang-up of the adapter's
// terminal does not reach it. A systemd user scope moves it out of the adapter's
// control group too, so that stopping the daemon's unit does not take it along.
// But a scope belongs to the user manager, which ends at the last logout of a
// user without linger, and a daemon started from a login session (SSH, a terminal
// multiplexer) is not in the user manager: there a process in a session of its own
// survives the logout and a scope would not. So the scope is used when the user
// manager can be reached and either linger keeps it alive or the daemon itself is
// under it, in which case the scope adds no risk.
var host struct {
	once       sync.Once
	systemdRun string // absolute path; empty when the process is not put in a scope
	detachment Detachment
}

// probeTimeout bounds each command the probe runs.
const probeTimeout = 5 * time.Second

func hostProbe() {
	host.once.Do(func() {
		host.systemdRun, host.detachment = probeHost()
	})
}

func hostDetachment() Detachment {
	hostProbe()
	return host.detachment
}

func probeHost() (systemdRun string, d Detachment) {
	const sessionOnly = "started in a session of its own but not moved out of the daemon's control group, " +
		"so a service manager that stops the daemon's unit, or the end of the login session, ends it"
	path, err := exec.LookPath("systemd-run")
	if err != nil {
		return "", Detachment{OutlivesDaemon: true, Caveat: "systemd-run is not installed: " + sessionOnly}
	}
	if path, err = filepath.Abs(path); err != nil {
		return "", Detachment{OutlivesDaemon: true, Caveat: "systemd-run cannot be located: " + sessionOnly}
	}
	if out, err := runProbe(path, "--user", "--scope", "--quiet", "--", "true"); err != nil {
		reason := firstLine(out)
		if reason == "" {
			reason = err.Error()
		}
		return "", Detachment{OutlivesDaemon: true, Caveat: "the systemd user manager is not usable (" + reason + "): " + sessionOnly}
	}
	linger := lingerEnabled()
	underManager := false
	if cgroup, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		underManager = underUserManager(string(cgroup), os.Getuid())
	}
	return chooseScope(path, linger, underManager)
}

// chooseScope applies the rule above to what the host answered: the user manager
// is usable (systemdRun is its command) and whether linger is enabled and the
// adapter is under the user manager. It returns the command to put the process in
// a scope with, empty when the process is not put in one, and says why in the
// caveat.
func chooseScope(systemdRun string, linger, underManager bool) (string, Detachment) {
	switch {
	case linger:
		return systemdRun, Detachment{OutlivesDaemon: true,
			Caveat: "runs in a systemd user scope of its own; linger is enabled, so it survives the last logout"}
	case underManager:
		return systemdRun, Detachment{OutlivesDaemon: true,
			Caveat: "runs in a systemd user scope of its own; linger is not enabled (loginctl enable-linger), " +
				"so it ends with the user manager at the last logout, as the daemon does"}
	}
	return "", Detachment{OutlivesDaemon: true,
		Caveat: "started in a session of its own, not in a systemd user scope, because the daemon is in a login " +
			"session and a scope would end at the last logout, which the process outlives this way; " +
			"a service manager that stops the daemon's unit, or the end of the login session, can still end it"}
}

// underUserManager reports whether the contents of /proc/<pid>/cgroup put a
// process under the systemd user manager of uid.
func underUserManager(cgroup string, uid int) bool {
	marker := "/user@" + strconv.Itoa(uid) + ".service/"
	for _, line := range strings.Split(cgroup, "\n") {
		// hierarchy-ID:controller-list:path; the path may hold colons.
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && strings.Contains(parts[2], marker) {
			return true
		}
	}
	return false
}

func lingerEnabled() bool {
	out, err := runProbe("loginctl", "show-user", "--property=Linger", "--value")
	return err == nil && strings.TrimSpace(out) == "yes"
}

// runProbe runs a short command with the adapter's environment and returns its
// combined output.
func runProbe(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// place says how the process is started here: wrapped in systemd-run when the
// host puts processes in a scope, as asked otherwise. systemd-run executes the
// command in place, so the PID the start returns is the process's.
//
// A wrapped command that cannot be executed would fail inside systemd-run, after
// the start succeeded, and show as an exit status. The executable is checked
// beforehand, so that a start fails the same way with a scope and without.
func place(spec Spec) ([]string, adapterproto.Container, Detachment, error) {
	hostProbe()
	if host.systemdRun == "" {
		return spec.Argv, adapterproto.Container{Kind: adapterproto.ContainerProcessGroup}, host.detachment, nil
	}
	path := spec.Argv[0]
	if !filepath.IsAbs(path) && spec.Dir != "" {
		path = filepath.Join(spec.Dir, path)
	}
	if err := unix.Access(path, unix.X_OK); err != nil {
		return nil, adapterproto.Container{}, Detachment{}, &os.PathError{Op: "fork/exec", Path: spec.Argv[0], Err: err}
	}
	unit := unitName(spec.Argv[0])
	wrapped := append([]string{host.systemdRun, "--user", "--scope", "--quiet", "--unit=" + unit, "--"}, spec.Argv...)
	return wrapped, adapterproto.Container{Kind: adapterproto.ContainerSystemdUnit, Name: unit}, host.detachment, nil
}

// unitName makes the name of the scope: the executable's base name with every
// character a unit name may not hold replaced, and 16 random hex digits so that
// two starts of one program do not meet.
func unitName(executable string) string {
	base := strings.TrimSuffix(filepath.Base(executable), ".exe")
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		b.WriteString("process")
	}
	var salt [8]byte
	_, _ = rand.Read(salt[:]) // never fails since Go 1.24
	return "sidera-" + b.String() + "-" + hex.EncodeToString(salt[:]) + ".scope"
}
