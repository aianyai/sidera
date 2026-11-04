package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
)

// Terminate ends only a server whose record is shown to name the process that
// runs. A record that cannot be shown, or a process table that does not answer,
// leaves the process alone and says why.
func TestTerminateEndsOnlyWhatTheRecordProves(t *testing.T) {
	const pid, startKey = 4242, "key-1"
	started := time.Unix(1_700_000_000, 123)
	here := process.Process{Identity: process.Identity{PID: pid, StartKey: startKey}, StartTime: started}
	tableFailure := errors.New("the process table did not answer")

	tests := []struct {
		name     string
		record   *state // nil: no state file
		running  func(process.Identity) (bool, error)
		lookup   func(int) (process.Process, bool, error)
		wantEnd  bool
		wantErr  error // matched with errors.Is; nil for success
		wantKey  string
		wantKept string // start_key the record holds afterwards
	}{
		{
			name:    "no record",
			record:  nil,
			wantErr: nil,
		},
		{
			name:     "recorded start key, same process runs",
			record:   &state{PID: pid, Address: "a", StartKey: startKey},
			running:  func(process.Identity) (bool, error) { return true, nil },
			wantEnd:  true,
			wantKey:  startKey,
			wantKept: startKey,
		},
		{
			name:     "recorded start key, the process has ended or the pid is another's",
			record:   &state{PID: pid, Address: "a", StartKey: startKey},
			running:  func(process.Identity) (bool, error) { return false, nil },
			wantKept: startKey,
		},
		{
			name:     "recorded start key, the process table does not answer",
			record:   &state{PID: pid, Address: "a", StartKey: startKey},
			running:  func(process.Identity) (bool, error) { return false, tableFailure },
			wantErr:  tableFailure,
			wantKept: startKey,
		},
		{
			name:    "old record, start time equals the process's: ours, and the key is written",
			record:  &state{PID: pid, Address: "a", StartedNS: started.UnixNano()},
			lookup:  func(int) (process.Process, bool, error) { return here, true, nil },
			wantEnd: true, wantKey: startKey, wantKept: startKey,
		},
		{
			name:    "old record, start time differs by a nanosecond: not proven",
			record:  &state{PID: pid, Address: "a", StartedNS: started.UnixNano() + 1},
			lookup:  func(int) (process.Process, bool, error) { return here, true, nil },
			wantErr: ErrNotProven,
		},
		{
			name:    "old record without a start time: not proven",
			record:  &state{PID: pid, Address: "a"},
			lookup:  func(int) (process.Process, bool, error) { return here, true, nil },
			wantErr: ErrNotProven,
		},
		{
			name:   "old record, no process holds the pid",
			record: &state{PID: pid, Address: "a", StartedNS: started.UnixNano()},
			lookup: func(int) (process.Process, bool, error) { return process.Process{}, false, nil },
		},
		{
			name:    "old record, the process table does not answer",
			record:  &state{PID: pid, Address: "a", StartedNS: started.UnixNano()},
			lookup:  func(int) (process.Process, bool, error) { return process.Process{}, false, tableFailure },
			wantErr: tableFailure,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, stateFileName)
			if tc.record != nil {
				raw, _ := json.Marshal(tc.record)
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			srv := New(Config{Program: "codex", StateDir: dir})
			srv.running = tc.running
			srv.lookup = tc.lookup
			var ended []process.Identity
			srv.terminate = func(_ context.Context, id process.Identity, grace time.Duration) error {
				if grace <= 0 {
					t.Errorf("grace %v: a polite step is expected", grace)
				}
				ended = append(ended, id)
				return nil
			}

			err := srv.Terminate(context.Background())
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("Terminate = %v, want %v", err, tc.wantErr)
			}
			switch {
			case tc.wantEnd && (len(ended) != 1 || ended[0] != process.Identity{PID: pid, StartKey: tc.wantKey}):
				t.Fatalf("terminate called with %v, want once with pid %d and key %q", ended, pid, tc.wantKey)
			case !tc.wantEnd && len(ended) != 0:
				t.Fatalf("terminate called with %v, want no call", ended)
			}
			if tc.record == nil {
				return
			}
			var kept state
			raw, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(raw, &kept) != nil {
				t.Fatalf("the state file is unreadable afterwards: %v", err)
			}
			if kept.StartKey != tc.wantKept || kept.PID != pid || kept.Address != "a" || kept.StartedNS != tc.record.StartedNS {
				t.Fatalf("the state file is %+v afterwards, want key %q and the rest as it was", kept, tc.wantKept)
			}
		})
	}
}

// A process that does not end is reported, and one that ended meanwhile is not
// an error.
func TestTerminateReportsWhatTheSDKReports(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(state{PID: 7, Address: "a", StartKey: "k"})
	if err := os.WriteFile(filepath.Join(dir, stateFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	stuck := errors.New("still runs")
	for _, tc := range []struct{ sdk, want error }{
		{process.ErrNotRunning, nil},
		{stuck, stuck},
	} {
		srv := New(Config{Program: "codex", StateDir: dir})
		srv.running = func(process.Identity) (bool, error) { return true, nil }
		srv.terminate = func(context.Context, process.Identity, time.Duration) error { return tc.sdk }
		if err := srv.Terminate(context.Background()); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("SDK answers %v: Terminate = %v, want %v", tc.sdk, err, tc.want)
		}
	}
}

// A server this call started that never answers is ended even when the state file
// could not be written: the stale or missing record must not decide which process
// is ended, because the start returned the identity of the one to end.
func TestStartEndsAServerThatNeverAnswersWhenTheStateFileCannotBeWritten(t *testing.T) {
	dir := shortTempDir(t)
	// A directory where the state file's temporary copy goes makes writing the file fail.
	if err := os.Mkdir(filepath.Join(dir, stateFileName+".tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	srv := New(Config{
		Program:      os.Args[0],
		StateDir:     dir,
		Environ:      append(os.Environ(), hangFileEnv+"="+pidFile),
		StartTimeout: 500 * time.Millisecond,
	})
	srv.userCacheDir = func() (string, error) { return t.TempDir(), nil }
	srv.probe = func(context.Context, appserver.Endpoint) error { return errors.New("not up") }

	if _, err := srv.Connect(context.Background()); !errors.Is(err, ErrStartFailed) {
		t.Fatalf("Connect: %v, want the start to fail for want of an answer", err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the stand-in did not run: %v", err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
	if _, found, err := process.Lookup(pid); err != nil || found {
		t.Fatalf("the server that never answered still runs: found %v, %v", found, err)
	}
}
