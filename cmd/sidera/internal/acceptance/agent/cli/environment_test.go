package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("SIDERA_DRIVER_HELPER") == "1" {
		if os.Getenv("SIDERA_DRIVER_EXIT") != "" {
			os.Exit(7)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"seed": os.Getenv("SIDERA_TEST_SEED"), "home": os.Getenv("SIDERA_HOME")})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestEachCallHasItsOwnEnvironment(t *testing.T) {
	n := 0
	a := New(Config{Binary: os.Args[0], Home: "test-home", Env: func() []string {
		n++
		return []string{"SIDERA_DRIVER_HELPER=1", "SIDERA_TEST_SEED=" + strconv.Itoa(n)}
	}})
	for i := 1; i <= 2; i++ {
		var out map[string]string
		if _, err := a.call(context.Background(), nil, nil, &out); err != nil {
			t.Fatal(err)
		}
		if out["seed"] != strconv.Itoa(i) || out["home"] != "test-home" {
			t.Fatalf("call %d environment = %v", i, out)
		}
	}
}

func TestAbnormalExitRetainsProcessEvidence(t *testing.T) {
	a := New(Config{Binary: os.Args[0], Env: func() []string {
		return []string{"SIDERA_DRIVER_HELPER=1", "SIDERA_DRIVER_EXIT=1"}
	}})
	_, err := a.call(context.Background(), nil, nil, new(any))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatal(fmt.Sprintf("abnormal exit evidence lost: %v", err))
	}
}
