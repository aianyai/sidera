package manageapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/daemonerr"
	"github.com/aianyai/sidera/internal/node/engine"
)

// restartRig is a server that can restart: what the check finds is the program and
// version it is given, or the error.
func restartRig(t *testing.T, version string, checkErr error) *rig {
	t.Helper()
	r := newRig(t, func(s *Server) {
		s.cfg.StartID = "0011223344556677"
		s.cfg.CheckRestart = func(context.Context) (string, string, error) { return "/opt/sidera/sidera", version, checkErr }
	})
	return r
}

// What the check of a restart finds is judged in the words of the wire, once, here;
// the engine runs the check (not for a resend) and hands the refusal on as it is,
// which the daemon's tests show end to end.
func TestTheCheckOfARestartIsWordedForTheWire(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expect   string
		checkErr error
		version  string
		category string
		code     string
	}{
		{"the program cannot outlive the daemon", "", fmt.Errorf("%w: the job ends it", daemonerr.ErrRestartUnavailable), "", "", "restart_unavailable"},
		{"the program does not answer", "", errors.New("exit status 2"), "", "refused", "restart_check_failed"},
		{"another version than expected", "0.12.0", nil, "0.11.0", "conflict", "restart_version_mismatch"},
		{"the version expected", "0.12.0", nil, "0.12.0", "", ""},
		{"no version expected", "", nil, "0.11.0", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := restartRig(t, tc.version, tc.checkErr)
			program, version, err := r.srv.restartCheck(tc.expect)(context.Background())
			if tc.code == "" {
				if err != nil || program != "/opt/sidera/sidera" || version != tc.version {
					t.Errorf("accepted check: %q, %q, %v", program, version, err)
				}
				return
			}
			if tc.category == "" {
				if !errors.Is(err, daemonerr.ErrRestartUnavailable) {
					t.Errorf("error = %v; want the sentinel, which the wire maps to %s", err, tc.code)
				}
				return
			}
			got := apiwire.FromError(err)
			if string(got.Category) != tc.category || got.Code != tc.code {
				t.Errorf("error = %v; want %s/%s", got, tc.category, tc.code)
			}
		})
	}
}

// An accepted restart is answered first and asked for once, as a restart and not as
// a stop. The words of its result are made by the engine's operation, so they are
// read in the tests of the daemon, which has the engine.
func TestRestartIsAnsweredThenAskedFor(t *testing.T) {
	r := restartRig(t, "0.12.0", nil)
	r.svc.execute = func(_ context.Context, c engine.ManagementCommand) (engine.StoredResult, error) {
		return engine.StoredResult{Result: []byte(`{"restarting":true}`), Ending: endingOf(c.Name)}, nil
	}
	status, out := r.post("daemon.restart", `{"operation_id":"`+opID(1)+`","params":{"expect_version":"0.12.0"}}`)
	if status != 200 || !strings.Contains(string(out), `"restarting":true`) {
		t.Fatalf("answered %d %s", status, out)
	}
	select {
	case <-r.restart.ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the restart was never asked for")
	}
	if r.stop.count() != 0 {
		t.Error("a restart asked for a stop")
	}
}
