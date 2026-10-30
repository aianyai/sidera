package fakeadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

type countedInput struct{ reads atomic.Int64 }

func (r *countedInput) Read([]byte) (int, error) { r.reads.Add(1); return 0, io.EOF }

type countedOutput struct{ writes atomic.Int64 }

func (w *countedOutput) Write(b []byte) (int, error) { w.writes.Add(1); return len(b), nil }
func (*countedOutput) Close() error                  { return nil }

func TestInputAttachCancellationPreventsServe(t *testing.T) {
	for _, closeControl := range []bool{false, true} {
		name := "caller cancellation"
		if closeControl {
			name = "control closure"
		}
		t.Run(name, func(t *testing.T) {
			f := newProcFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waitCtx, stop := context.WithTimeout(context.Background(), waitLimit)
			defer stop()
			entered := make(chan struct{})
			handlerDone := make(chan struct{})
			f.srv.Handle(fakeadapter.MethodInputsAttach, func(ctx context.Context, _ *fakectl.Conn, _ json.RawMessage) (any, error) {
				close(entered)
				defer close(handlerDone)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			f.cfg.RestoreInputs = true
			input, output := &countedInput{}, &countedOutput{}
			done := make(chan error, 1)
			go func() { done <- fakeadapter.RunProcess(ctx, f.cfg, input, output, nil) }()
			select {
			case <-entered:
			case <-waitCtx.Done():
				t.Fatal("attach did not start")
			}
			if closeControl {
				if err := f.srv.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("failed attach returned success")
				}
			case <-waitCtx.Done():
				t.Fatal("attach did not terminate")
			}
			select {
			case <-handlerDone:
			case <-waitCtx.Done():
				t.Fatal("cancelled control handler leaked")
			}
			if input.reads.Load() != 0 || output.writes.Load() != 0 {
				t.Fatal("protocol stream served before attach completed")
			}
			log, err := ledger.ReadDir(f.cfg.LedgerDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(log.Kind(ledger.KindAdapterStarted)) != 0 {
				t.Fatal("Serve started before attach acknowledgement")
			}
		})
	}
}

func TestBinaryControlClosureCancelsAttachWaitingOnInstance(t *testing.T) {
	f := newProcFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	owner, err := fakeadapter.NewInputRegistry([]string{"fake"}, map[string]fakeadapter.ExternalInputs{"fake": declaredInputs()})
	if err != nil {
		t.Fatal(err)
	}
	owner.HandleControl(f.srv)
	f.cfg.RestoreInputs = true
	entered := make(chan struct{})
	blocker := &inputReceiver{apply: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	held, release := context.WithCancel(ctx)
	defer release()
	blocked := make(chan error, 1)
	go func() { _, err := owner.Attach(held, "fake", blocker); blocked <- err }()
	<-entered
	a := f.startAdapter()
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.exited:
	case <-ctx.Done():
		t.Fatal("binary survived control closure during attach")
	}
	release()
	if err := <-blocked; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked receiver cancellation: %v", err)
	}
	log, err := ledger.ReadDir(f.cfg.LedgerDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Kind(ledger.KindAdapterStarted)) != 0 {
		t.Fatal("binary served before installation completed")
	}
	// The old failed installation released its boundary; a new receiver can attach.
	d, err := owner.Attach(ctx, "fake", &inputReceiver{})
	if err != nil {
		t.Fatal(err)
	}
	d()
}
