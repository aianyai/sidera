package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver/appservertest"
)

// On Windows a recorded server that is running but does not answer a probe is
// probed again and never replaced: starting a second server would overwrite the
// token of the first. A recorded process that is gone, or is not the one that was
// started, is replaced. The program is a name that does not exist, so a start shows
// as ErrStartFailed.
func TestConnectWindowsRecordedServer(t *testing.T) {
	const pid, startedNS = 4242, 1_000_000

	tests := []struct {
		name string
		// failProbes is how many probes fail before the fake server answers;
		// -1 means all do.
		failProbes int
		running    bool
		started    int64 // start time of the process now holding pid
		wantStart  bool
		wantErr    string // substring of the error, empty for success
	}{
		{name: "answers on a later probe", failProbes: 2, running: true, started: startedNS},
		{name: "never answers", failProbes: -1, running: true, started: startedNS, wantErr: "pid 4242"},
		{name: "process is gone", failProbes: -1, running: false, wantStart: true},
		{name: "pid belongs to another process", failProbes: -1, running: true, started: startedNS + 1, wantStart: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := appservertest.NewTCP(t, "old-token")
			defer fake.Close()
			dir := t.TempDir()
			raw, _ := json.Marshal(state{PID: pid, Address: fake.Endpoint.Address, StartedNS: startedNS})
			tokenPath := filepath.Join(dir, tokenFileName)
			if err := os.WriteFile(filepath.Join(dir, stateFileName), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tokenPath, []byte("old-token"), 0o600); err != nil {
				t.Fatal(err)
			}

			srv := New(Config{Program: filepath.Join(dir, "no-such-codex"), StateDir: dir, StartTimeout: time.Second})
			srv.goos = "windows"
			srv.userCacheDir = func() (string, error) { return dir, nil }
			srv.retryWaits = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
			srv.processStart = func(p int) (int64, bool) { return tc.started, p == pid && tc.running }
			probes := 0
			srv.probe = func(ctx context.Context, ep appserver.Endpoint) error {
				probes++
				if tc.failProbes < 0 || probes <= tc.failProbes {
					return errors.New("probe failed")
				}
				return appserver.Probe(ctx, ep)
			}

			ep, err := srv.Connect(context.Background())
			switch {
			case tc.wantStart:
				if !errors.Is(err, ErrStartFailed) {
					t.Fatalf("Connect: %v, want a start attempt", err)
				}
				return
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || errors.Is(err, ErrStartFailed) {
					t.Fatalf("Connect: %v, want an error naming %q and no start", err, tc.wantErr)
				}
			case err != nil:
				t.Fatalf("Connect: %v", err)
			case ep != fake.Endpoint:
				t.Fatalf("endpoint %+v, want %+v", ep, fake.Endpoint)
			}
			if got, _ := os.ReadFile(tokenPath); string(got) != "old-token" {
				t.Errorf("the token of the running server was overwritten with %q", got)
			}
		})
	}
}
