package swap_test

import (
	"bufio"
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/update/swap"
	"github.com/aianyai/sidera/internal/node/fsx"
)

const holdEnv = "SWAP_TEST_HOLD"

// TestMain makes the test binary double as a program that runs until its input
// ends, to stand in for the daemon that runs from the program being replaced.
func TestMain(m *testing.M) {
	if os.Getenv(holdEnv) != "" {
		os.Stdout.WriteString("running\n")
		io.Copy(io.Discard, os.Stdin)
		return
	}
	os.Exit(m.Run())
}

// The one platform assumption of the order of the swap: a program that is
// running can be renamed to .old and replaced, on the real file system. On
// Windows a running program cannot be overwritten or removed, and its .old
// cannot be removed until it has ended, so a following update must still
// succeed with that .old in place.
func TestRunningProgramIsReplacedOnTheRealFileSystem(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	bin, cache := t.TempDir(), t.TempDir()
	name := "sidera"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "sidera"), []byte("new program"), 0o700); err != nil {
		t.Fatal(err)
	}

	child := exec.Command(filepath.Join(bin, name))
	child.Env = append(os.Environ(), holdEnv+"=1")
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	ended := false
	end := func() {
		if !ended {
			ended = true
			stdin.Close()
			child.Wait()
		}
	}
	defer end()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "running\n" {
		t.Fatalf("the program did not start: %q, %v", line, err)
	}

	dst, err := fsx.OpenOS(bin, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	src, err := fsx.OpenOSReader(cache)
	if err != nil {
		t.Fatal(err)
	}
	update := func(program string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cache, "sidera"), []byte(program), 0o700); err != nil {
			t.Fatal(err)
		}
		s, err := swap.Prepare(dst, src, []swap.Item{{Target: name, Source: "sidera"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	read := func(file string) []byte {
		got, _ := os.ReadFile(filepath.Join(bin, file))
		return got
	}

	update("new program")
	if got := read(name); string(got) != "new program" {
		t.Errorf("%s holds %d bytes, want the new program", name, len(got))
	}
	if !slices.Equal(read(name+".old"), original) {
		t.Errorf("%s.old does not hold the program that was running", name)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(bin, name)); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("new program: %v, %v; want mode 0700", fi, err)
		}
	}

	// The next update starts with Clean while the first program still runs. On
	// Windows its .old cannot be removed, so it is renamed out of the way: the
	// name .old must be free for the second Commit.
	cleaned, err := swap.Clean(dst, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	var wantLeft []string
	if runtime.GOOS == "windows" {
		wantLeft = []string{name + ".old-1"}
	}
	if !slices.Equal(cleaned.Left, wantLeft) {
		t.Errorf("Clean while the old program runs left %v, want %v", cleaned.Left, wantLeft)
	}
	if _, err := os.Stat(filepath.Join(bin, name+".old")); !os.IsNotExist(err) {
		t.Errorf("%s.old is not free after Clean: %v", name, err)
	}
	update("newer program")
	if got := read(name); string(got) != "newer program" {
		t.Errorf("%s holds %q after the second update, want the newer program", name, got)
	}
	if got := read(name + ".old"); string(got) != "new program" {
		t.Errorf("%s.old holds %q, want the program the second update replaced", name, got)
	}
	if runtime.GOOS == "windows" && !slices.Equal(read(name+".old-1"), original) {
		t.Errorf("%s.old-1 does not hold the program that is running", name)
	}

	end()
	cleaned, err = swap.Clean(dst, []string{name})
	if err != nil || len(cleaned.Left) != 0 {
		t.Errorf("Clean after it ended = %v, %v; want nothing left", cleaned, err)
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Errorf("bin holds %v after Clean, want only %s", entries, name)
	}
}
