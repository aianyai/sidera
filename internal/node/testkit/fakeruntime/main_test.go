package fakeruntime_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// helperEnv switches the test binary into a helper program: the command an
// ExecRunner test starts. It prints one line made of its arguments and of the
// session variable and, when it was given one, the test seed.
const helperEnv = "FAKERUNTIME_TEST_HELPER"

var (
	binDir   string
	buildErr error
	buildOne sync.Once
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		if os.Args[len(os.Args)-1] == "page" {
			// What the client prints for a receive: one page with one message.
			b, _ := json.Marshal(adapterproto.RecvPage{Lease: leaseA, Messages: []adapterproto.PageMessage{{ID: "m1", From: "alice/agent", Body: "hello"}}})
			fmt.Println(string(b))
			os.Exit(0)
		}
		out := fmt.Sprintf("helper %s token=%s", os.Args[len(os.Args)-1], os.Getenv("SIDERA_TOKEN"))
		if seed := os.Getenv("SIDERA_TEST_SEED"); seed != "" {
			out += " seed=" + seed
		}
		fmt.Println(out)
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
