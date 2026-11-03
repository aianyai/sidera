package process_test

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
	"golang.org/x/sys/unix"
)

func init() {
	// nondumpable says it is ready after it made itself non-dumpable, as an agent
	// runtime may, which makes the kernel report /proc/<pid> as owned by root.
	childModes["nondumpable"] = func() {
		if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
			println("prctl:", err.Error())
			os.Exit(2)
		}
		println("ready")
		time.Sleep(childLifetime)
	}
}

// A process the adapter started is listed whatever the owner of its /proc
// directory is; List is how an adapter finds the processes it started before a
// restart.
func TestListIncludesANonDumpableProcessOfTheUser(t *testing.T) {
	c := startChild(t, "nondumpable", nil)
	c.waitOutput(t, "ready")
	all, err := process.List()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(all, func(p process.Process) bool { return p.Identity == c.Identity }) {
		t.Fatalf("List lacks the non-dumpable process %+v", c.Identity)
	}
}

// checkScope: the process is in the scope Start named. The scope is made by
// systemd-run, which becomes the process only after it has set the scope up, so
// the test child is waited for first. Hosts without a user manager (the CI
// runners) never take this path.
func checkScope(t *testing.T, c child) {
	t.Helper()
	path := "/proc/" + strconv.Itoa(c.PID) + "/cgroup"
	var cgroup []byte
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		cgroup, _ = os.ReadFile(path)
		if strings.Contains(string(cgroup), c.Container.Name) {
			return
		}
	}
	t.Fatalf("%s is %q, want it to hold the unit %q", path, cgroup, c.Container.Name)
}
