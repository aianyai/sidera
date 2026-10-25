package fakeadapter_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	binDir   string
	buildErr error
	buildOne sync.Once
)

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
	os.Exit(code)
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// programs builds the fake adapter and the fake runtime once per test run and
// returns the paths of the two programs.
func programs(t testing.TB) (adapter, rt string) {
	t.Helper()
	buildOne.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "fakeadapter-bin-")
		if buildErr != nil {
			return
		}
		for _, name := range []string{"sidera-fake-adapter", "sidera-fake-runtime"} {
			cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, exe(name)), "github.com/aianyai/sidera/cmd/"+name)
			if out, err := cmd.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("go build %s: %v\n%s", name, err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatalf("cannot build the fake programs: %v", buildErr)
	}
	return filepath.Join(binDir, exe("sidera-fake-adapter")), filepath.Join(binDir, exe("sidera-fake-runtime"))
}
