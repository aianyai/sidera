package fakeruntime_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// helperEnv switches the test binary into a helper program: the command an
// ExecRunner test starts. It prints one line made of its arguments and of the
// session variable it was given.
const helperEnv = "FAKERUNTIME_TEST_HELPER"

var (
	binDir   string
	buildErr error
	buildOne sync.Once
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		fmt.Printf("helper %s token=%s\n", os.Args[len(os.Args)-1], os.Getenv("SIDERA_TOKEN"))
		os.Exit(0)
	}
	code := m.Run()
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// runtimeBinary builds the fake runtime program once per test run.
func runtimeBinary(t testing.TB) string {
	t.Helper()
	buildOne.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "fakeruntime-bin-")
		if buildErr != nil {
			return
		}
		name := "sidera-fake-runtime"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, name), "github.com/aianyai/sidera/cmd/sidera-fake-runtime")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatalf("cannot build the fake runtime program: %v", buildErr)
	}
	name := "sidera-fake-runtime"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(binDir, name)
}
