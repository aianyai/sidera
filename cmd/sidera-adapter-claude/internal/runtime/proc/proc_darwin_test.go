package proc

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// The start key of a process on macOS is "<sec>.<usec, 6 digits>" of the kernel's
// p_starttime, the text the first versions of the adapter stored in checkpoints; a
// key of another form would make every stored turn process look like a reused PID.
func TestStartKeyOnMacOSIsTheFormCheckpointsHold(t *testing.T) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pid", os.Getpid())
	if err != nil || len(kps) == 0 {
		t.Fatalf("sysctl: %v", err)
	}
	st := kps[0].Proc.P_starttime
	want := fmt.Sprintf("%d.%06d", st.Sec, st.Usec)
	info, ok, err := NewTable().Lookup(os.Getpid())
	if err != nil || !ok {
		t.Fatalf("Lookup(self) = %v, %v", ok, err)
	}
	if info.StartKey != want || info.NativeStart != want || info.StartUnix != int64(st.Sec) {
		t.Fatalf("info = %+v, want start key %q and start %d", info, want, st.Sec)
	}
}
