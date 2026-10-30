package fakeruntime_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

const launcherHelperEnv = "FAKERUNTIME_STARTUP_HELPER"

// The helper gives the launcher a real parent process that the test can lose
// while its detached native child is still completing the control handshake.
func TestPendingStartupLauncherHelper(t *testing.T) {
	if os.Getenv(launcherHelperEnv) == "" {
		return
	}
	var cfg struct {
		Binary string
		Spec   fakeruntime.ProcessSpec
	}
	if err := json.Unmarshal([]byte(os.Getenv(launcherHelperEnv)), &cfg); err != nil {
		t.Fatal("cannot decode launcher helper input")
	}
	clock := fakectl.NewClock(cfg.Spec.ClockStartMS)
	l := &fakeruntime.ProcessLauncher{Binary: cfg.Binary, Dir: cfg.Spec.Dir, LedgerDir: cfg.Spec.LedgerDir, Control: cfg.Spec.Control, Seed: cfg.Spec.Seed, Now: clock.Now}
	defer l.Close()
	if _, err := l.Create(t.Context(), cfg.Spec.Create); err != nil {
		t.Fatal("launcher helper failed to create its session")
	}
}

func TestPendingStartupSurvivesLauncherLoss(t *testing.T) {
	for _, loss := range []string{"parent exit", "caller cancellation"} {
		t.Run(loss, func(t *testing.T) {
			e := newProcEnv(t)
			ctx, stop := context.WithTimeout(t.Context(), limit)
			defer stop()
			gate := holdRuntimeHello(t, e.srv.Addr())
			e.launcher.Control = gate.addr
			req := fakeruntime.CreateRequest{Attempt: attempt(1), Endpoint: "sess-1", Preset: "default", FirstAttempt: attempt(1), FirstPrompt: "hello", Env: map[string]string{"SIDERA_TOKEN": "startup-private-session"}}
			var loseLauncher func()
			if loss == "parent exit" {
				raw, err := json.Marshal(struct {
					Binary string
					Spec   fakeruntime.ProcessSpec
				}{e.launcher.Binary, fakeruntime.ProcessSpec{Create: req, Dir: e.dir, LedgerDir: e.ledgers, Control: gate.addr, Seed: 9, ClockStartMS: e.clock.Now().UnixMilli()}})
				if err != nil {
					t.Fatal("cannot encode launcher helper input")
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestPendingStartupLauncherHelper$")
				cmd.Env = append(os.Environ(), launcherHelperEnv+"="+string(raw))
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				var once sync.Once
				loseLauncher = func() {
					once.Do(func() {
						if err := cmd.Process.Kill(); err != nil {
							t.Error(err)
						}
						if err := cmd.Wait(); err == nil {
							t.Error("launcher helper exited normally instead of being killed")
						}
					})
				}
				t.Cleanup(loseLauncher)
			} else {
				createCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := e.launcher.Create(createCtx, req); done <- err }()
				loseLauncher = func() {
					cancel()
					select {
					case err := <-done:
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("pending Create = %v, want cancellation", err)
						}
					case <-ctx.Done():
						t.Fatal("pending Create did not observe cancellation")
					}
				}
			}
			var hello fakectl.HelloParams
			select {
			case hello = <-gate.hello:
			case <-ctx.Done():
				t.Fatal("native did not reach its control handshake")
			}
			ref := nativeRef(9, req.Attempt)
			if hello.ID != ref || hello.Role != fakectl.RoleRuntime || hello.PID < 1 {
				t.Fatal("pending handshake does not identify the actual native")
			}
			if _, err := os.Stat(filepath.Join(e.dir, ref+".jsonl")); err != nil {
				t.Fatal("native did not claim its record before the handshake")
			}
			if _, err := os.Stat(filepath.Join(e.dir, ref+".json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("native published readiness before its handshake completed")
			}
			loseLauncher()
			again := &fakeruntime.ProcessLauncher{Binary: e.launcher.Binary, Dir: e.dir, LedgerDir: e.ledgers, Control: gate.addr, Seed: 9, Now: e.clock.Now}
			defer again.Close()
			type result struct {
				session fakeruntime.Session
				err     error
			}
			done := make(chan result, 1)
			go func() { s, err := again.Create(ctx, req); done <- result{s, err} }()
			gate.release()
			var s fakeruntime.Session
			select {
			case r := <-done:
				if r.err != nil {
					t.Fatalf("replay of pending creation: %v", r.err)
				}
				s = r.session
			case <-ctx.Done():
				t.Fatal("replay did not take over the pending native")
			}
			raw, err := os.ReadFile(filepath.Join(e.dir, ref+".json"))
			if err != nil {
				t.Fatal(err)
			}
			info, err := fakeruntime.DecodeProcessInfo(raw)
			if err != nil {
				t.Fatal(err)
			}
			if s.Ref() != hello.ID || s.PID() != hello.PID || s.StartKey() != info.StartKey || info.PID != hello.PID || !s.Alive() {
				t.Fatal("replay did not return the same actual live native identity")
			}
			env, err := e.ctrl.SessionEnv(ref)
			if err != nil || !maps.Equal(env, req.Env) {
				t.Fatal("replayed native did not retain its actual session environment")
			}
			log, err := ledger.ReadDir(e.ledgers)
			if err != nil {
				t.Fatal(err)
			}
			created := log.Kind(ledger.KindRuntimeSessionCreated)
			if len(created) != 1 || created[0].PID != hello.PID || created[0].Attrs.NativeRef != ref {
				t.Fatal("replay did not preserve exactly one real native creation")
			}
			if !e.clock.Now().Equal(start) {
				t.Fatal("startup waiting advanced the logical clock")
			}
			for _, entry := range log.Entries() {
				if entry.AtMS != start.UnixMilli() {
					t.Fatal("startup used real time for a native event")
				}
			}
			conn, err := e.srv.Wait(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.srv.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-conn.Done():
			case <-ctx.Done():
				t.Fatal("native control connection did not close")
			}
			e.waitGone(ref)
		})
	}
}

func TestUnpublishedNativeDeathRetainsItsClaimUntilCallerCancellation(t *testing.T) {
	e := newProcEnv(t)
	ctx, stop := context.WithTimeout(t.Context(), limit)
	defer stop()
	gate := holdRuntimeHello(t, e.srv.Addr())
	e.launcher.Control = gate.addr
	req := fakeruntime.CreateRequest{Attempt: attempt(1), Endpoint: "sess-1", Preset: "default", FirstAttempt: attempt(1), FirstPrompt: "hello"}
	createCtx, cancelCreate := context.WithCancel(ctx)
	defer cancelCreate()
	done := make(chan error, 1)
	go func() { _, err := e.launcher.Create(createCtx, req); done <- err }()
	var hello fakectl.HelloParams
	select {
	case hello = <-gate.hello:
	case <-ctx.Done():
		t.Fatal("native did not reach its control handshake")
	}
	ref := nativeRef(9, req.Attempt)
	if hello.ID != ref || hello.Role != fakectl.RoleRuntime || hello.PID < 1 {
		t.Fatal("pending handshake does not identify the actual native")
	}
	recordPath := filepath.Join(e.dir, ref+".jsonl")
	originalFile, err := os.Stat(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	originalRecord, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	infoPath := filepath.Join(e.dir, ref+".json")
	if _, err := os.Stat(infoPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("native published readiness before its handshake completed")
	}
	conn, err := e.srv.Wait(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	// This PID came from the native this test started, while its real hello
	// response is still held. Its launcher remains the sole Wait owner.
	process, err := os.FindProcess(int(hello.PID))
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.Done():
	case <-ctx.Done():
		t.Fatal("killed native did not end its control connection")
	}
	cancelCreate()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending creation after native death = %v, want cancellation", err)
		}
	case <-ctx.Done():
		t.Fatal("pending creation did not observe cancellation")
	}
	again := &fakeruntime.ProcessLauncher{Binary: e.launcher.Binary, Dir: e.dir, LedgerDir: e.ledgers, Control: gate.addr, Seed: 9, Now: e.clock.Now}
	defer again.Close()
	// This deadline bounds the caller, not native startup. Actual death above
	// and the held handshake establish that no readiness can be published.
	replayCtx, cancelReplay := context.WithTimeout(ctx, time.Second)
	defer cancelReplay()
	if replayCtx.Err() != nil {
		t.Fatal("replay context ended before Create was called")
	}
	if s, err := again.Create(replayCtx, req); s != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("replay without publication = %v, want caller deadline and no session", err)
	}
	if _, err := os.Stat(infoPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a dead unpublished native acquired fictional readiness")
	}
	retainedFile, err := os.Stat(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	retainedRecord, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(originalFile, retainedFile) || !bytes.Equal(originalRecord, retainedRecord) {
		t.Fatal("unpublished native claim was deleted, replaced or changed")
	}
	log, err := ledger.ReadDir(e.ledgers)
	if err != nil {
		t.Fatal(err)
	}
	created := log.Kind(ledger.KindRuntimeSessionCreated)
	if len(created) != 1 || created[0].PID != hello.PID || created[0].Attrs.NativeRef != ref {
		t.Fatal("unpublished native replay produced a second creation effect")
	}
}

type runtimeHelloGate struct {
	addr    string
	hello   chan fakectl.HelloParams
	release func()
}

// This proxy holds only the real fixture's first hello response. The native has
// already created its record, but RunProcess cannot publish ProcessInfo yet.
func holdRuntimeHello(t *testing.T, target string) runtimeHelloGate {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	var once sync.Once
	g := runtimeHelloGate{addr: ln.Addr().String(), hello: make(chan fakectl.HelloParams, 1), release: func() { once.Do(func() { close(released) }) }}
	ctx, cancel := context.WithCancel(t.Context())
	var mu sync.Mutex
	var conns []net.Conn
	adopt := func(c net.Conn) {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			_ = c.Close()
		} else {
			conns = append(conns, c)
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		down, err := ln.Accept()
		if err != nil {
			return
		}
		adopt(down)
		defer down.Close()
		up, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
		if err != nil {
			return
		}
		adopt(up)
		defer up.Close()
		dr, ur := bufio.NewReader(down), bufio.NewReader(up)
		line, err := dr.ReadBytes('\n')
		if err != nil {
			return
		}
		var request struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &request) != nil || request.Method != fakectl.MethodHello {
			return
		}
		value, err := fakectl.DecodeParams(request.Method, request.Params)
		if err != nil {
			return
		}
		if _, err := up.Write(line); err != nil {
			return
		}
		response, err := ur.ReadBytes('\n')
		if err != nil {
			return
		}
		// Forward native EOF even while the hello answer is held, so fixture
		// departure remains observable if the native dies before publication.
		forwarded := make(chan struct{})
		go func() { _, _ = io.Copy(up, dr); _ = up.Close(); close(forwarded) }()
		defer func() {
			_ = down.Close()
			_ = up.Close()
			<-forwarded
		}()
		g.hello <- value.(fakectl.HelloParams)
		select {
		case <-released:
		case <-ctx.Done():
			return
		}
		if _, err := down.Write(response); err != nil {
			return
		}
		_, _ = io.Copy(down, ur)
	}()
	t.Cleanup(func() {
		cancel()
		g.release()
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return g
}
