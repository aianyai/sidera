package fakeruntime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
)

func TestSessionEnvReturnsOnlyTheLiveSessionsActualEnvironment(t *testing.T) {
	for _, process := range []bool{false, true} {
		name := "local"
		if process {
			name = "process"
		}
		t.Run(name, func(t *testing.T) {
			var launcher fakeruntime.Launcher
			var controller fakeruntime.Controller
			var recorded func() []byte
			if process {
				e := newProcEnv(t)
				launcher, controller = e.launcher, e.ctrl
				recorded = func() []byte { return readEnvRecords(t, e.dir, e.ledgers) }
			} else {
				e := newEnv(t, nil)
				launcher, controller = e.rt, e.rt
				recorded = func() []byte {
					raw, err := json.Marshal(e.log().Entries())
					if err != nil {
						t.Fatal("cannot encode ledger")
					}
					return append(raw, readEnvRecords(t, e.dir)...)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*limit)
			defer cancel()
			secrets := []string{"session-env-private-first", "session-env-private-second"}
			var sessions []fakeruntime.Session
			for i, token := range secrets {
				want := map[string]string{"SIDERA_TOKEN": token, "PROJECT": name}
				s, err := launcher.Create(ctx, fakeruntime.CreateRequest{Attempt: attempt(i + 1), Endpoint: "session", Preset: "default", FirstAttempt: attempt(i + 1), FirstPrompt: "hello", Env: want})
				if err != nil {
					t.Fatal("cannot create native session")
				}
				sessions = append(sessions, s)
			}
			for i, s := range sessions {
				want := map[string]string{"SIDERA_TOKEN": secrets[i], "PROJECT": name}
				got, err := controller.SessionEnv(s.Ref())
				if err != nil || !maps.Equal(got, want) {
					t.Fatal("session environment differs from actual creation input")
				}
				got["SIDERA_TOKEN"] = "changed"
				delete(got, "PROJECT")
				got["EXTRA"] = "changed"
				again, err := controller.SessionEnv(s.Ref())
				if err != nil || !maps.Equal(again, want) {
					t.Fatal("returned environment aliases the native session")
				}
				if err := s.Close(ctx); err != nil {
					t.Fatal("cannot close native session")
				}
				if env, err := controller.SessionEnv(s.Ref()); !errors.Is(err, fakeruntime.ErrNotAlive) || env != nil {
					t.Fatal("closed session returned an environment or the wrong error")
				}
			}
			if env, err := controller.SessionEnv("unknown"); !errors.Is(err, fakeruntime.ErrUnknownSession) || env != nil {
				t.Fatal("unknown session returned an environment or the wrong error")
			}
			for _, secret := range secrets {
				if bytes.Contains(recorded(), []byte(secret)) {
					t.Fatal("session credential reached a recordable output")
				}
			}
		})
	}
}

func readEnvRecords(t *testing.T, dirs ...string) []byte {
	t.Helper()
	var result []byte
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			result = append(result, raw...)
			return err
		})
		if err != nil {
			t.Fatal("cannot inspect recordable output")
		}
	}
	return result
}

func TestSessionEnvRejectsUntrustedControlReplies(t *testing.T) {
	for _, tc := range []struct{ rule, reply string }{
		{"unknown field", `{"ref":"n-0123abcd","env":{"SIDERA_TOKEN":"private"},"extra":1}`},
		{"duplicate key", `{"ref":"n-0123abcd","env":{"SIDERA_TOKEN":"first","SIDERA_TOKEN":"second"}}`},
		{"null environment", `{"ref":"n-0123abcd","env":null}`},
		{"absent environment", `{"ref":"n-0123abcd"}`},
		{"wrong native reference", `{"ref":"n-1123abcd","env":{"SIDERA_TOKEN":"private"}}`},
		{"private invalid map key", `{"ref":"n-0123abcd","env":{"private":42}}`},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			srv, err := fakectl.Listen(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Close()
			client, err := fakectl.Dial(ctx, srv.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			client.HandleKnob(func(context.Context, string, json.RawMessage) (any, error) { return json.RawMessage(tc.reply), nil })
			if err := client.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleRuntime, ID: "n-0123abcd", PID: 1}); err != nil {
				t.Fatal(err)
			}
			controller := fakeruntime.RemoteController{Server: srv}
			if env, err := controller.SessionEnv("n-0123abcd"); err == nil || env != nil {
				t.Fatal("accepted invalid or wrong-session environment response")
			} else if bytes.Contains([]byte(err.Error()), []byte("private")) {
				t.Fatal("control decoder exposed response contents")
			}
		})
	}
}

func TestSessionEnvDoesNotQueryAnAdapterWithTheSameID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	srv, err := fakectl.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	client, err := fakectl.Dial(ctx, srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: "n-0123abcd", PID: 1}); err != nil {
		t.Fatal(err)
	}
	controller := fakeruntime.RemoteController{Server: srv}
	if env, err := controller.SessionEnv("n-0123abcd"); !errors.Is(err, fakeruntime.ErrUnknownSession) || env != nil {
		t.Fatal("queried an adapter as a runtime session")
	}
}
