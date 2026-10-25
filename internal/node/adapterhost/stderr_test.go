package adapterhost_test

import (
	"io"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
)

// What an adapter writes on its standard error goes to adapter-<instance>.log, one
// line at a time, and nothing of the protocol does.
func TestTheStderrOfTheAdapterGoesToItsLog(t *testing.T) {
	t.Run("the lines, whole, and no protocol", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		var stderr io.Writer
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			stderr = inst.Stderr
			pp, _ := r.scriptedProcess(nil, false)
			return pp, nil
		}
		r.start()
		r.ready("chat", 1)
		// Bytes as a pipe delivers them: a line in two writes, two lines in one.
		for _, chunk := range []string{"starting up\npart", "ial line\nsecond\nthird\n", "no newline at the end"} {
			if n, err := stderr.Write([]byte(chunk)); err != nil || n != len(chunk) {
				t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
			}
		}
		if err := r.host.Shutdown(timeoutCtx(t)); err != nil {
			t.Fatal(err)
		}
		want := "starting up\npartial line\nsecond\nthird\nno newline at the end\n"
		if got := r.logs.read("adapter-chat.log"); got != want {
			t.Fatalf("adapter-chat.log = %q, want %q", got, want)
		}
		if got := r.logs.read("adapter-chat.log"); strings.Contains(got, "jsonrpc") {
			t.Fatalf("a protocol line is in the log: %q", got)
		}
		// After the end of the log a process that still writes is not failed by it.
		if n, err := stderr.Write([]byte("late\n")); err != nil || n != 5 {
			t.Fatalf("a write after the log was closed = %d, %v, want it dropped without error", n, err)
		}
		if got := r.logs.read("adapter-chat.log"); strings.Contains(got, "late") {
			t.Fatalf("a line written after the end reached the log: %q", got)
		}
	})

	t.Run("a log that cannot be opened is a warning, not a refusal", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.logs.failing = true
		var stderr io.Writer
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			stderr = inst.Stderr
			pp, _ := r.scriptedProcess(nil, false)
			return pp, nil
		}
		r.start()
		r.ready("chat", 1)
		if stderr == nil {
			t.Fatal("Instance.Stderr is nil")
		}
		if n, err := stderr.Write([]byte("lost\n")); err != nil || n != 5 {
			t.Fatalf("a write to a log that was not opened = %d, %v, want it discarded without error", n, err)
		}
		if !r.records.contains("log of the adapter cannot be opened") {
			t.Fatalf("the failure to open the log was not logged: %v", r.records.msgs)
		}
		if !r.host.Counts("chat").Connected {
			t.Fatal("an adapter whose log cannot be opened was refused")
		}
		if got := r.logs.opened; len(got) != 1 || got[0] != "adapter-chat.log" {
			t.Fatalf("logs opened: %v", got)
		}
	})
}
