package process_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
