package fakeadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// A native creation can fail to report after the real session already exists.
// This boundary fault leaves the actual runtime and its ledger in use.
type createErrorLauncher struct {
	fakeruntime.Launcher
	afterEffect bool
	err         error
	entered     chan struct{}
}

func (l createErrorLauncher) Create(ctx context.Context, req fakeruntime.CreateRequest) (fakeruntime.Session, error) {
	if l.afterEffect {
		if _, err := l.Launcher.Create(ctx, req); err != nil {
			return nil, err
		}
	}
	if l.entered != nil {
		close(l.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("native creation did not report: %w", l.err)
}

func TestCreateErrorsKeepTheNativeEffectUnknown(t *testing.T) {
	for _, tc := range []struct {
		name        string
		afterEffect bool
		err         error
	}{
		{"before creation", false, errors.New("native startup failed")},
		{"after creation", true, errors.New("native publication failed")},
		{"native application error after creation", true, peer.NewError(adapterproto.CodeEndpointLimit, "native request refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOptions{launcher: func(l fakeruntime.Launcher) fakeruntime.Launcher {
				return createErrorLauncher{Launcher: l, afterEffect: tc.afterEffect, err: tc.err}
			}})
			r.initialize()
			_, err := r.call(adapterproto.MethodSessionCreate, r.createParams(1, "sess-1"))
			if codeOf(err) != peer.CodeInternalError || peer.NoEffect(err) {
				t.Fatalf("creation error %v must leave the effect unknown", err)
			}
			want := 0
			if tc.afterEffect {
				want = 1
			}
			if got := len(r.ledgerLog().Kind(ledger.KindRuntimeSessionCreated)); got != want {
				t.Fatalf("native creations %d, want %d", got, want)
			}
			if got := r.ledgerLog().Kind(ledger.KindNativeCreateReturned); len(got) != 0 {
				t.Fatalf("unknown native creation recorded a definite result: %+v", got)
			}
		})
	}
}

func TestCreateCancellationRemainsCancellationAfterTheNativeEffect(t *testing.T) {
	entered := make(chan struct{})
	r := newRig(t, rigOptions{launcher: func(l fakeruntime.Launcher) fakeruntime.Launcher {
		return createErrorLauncher{Launcher: l, afterEffect: true, entered: entered}
	}})
	r.initialize()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var raw json.RawMessage
		done <- r.daemon.Call(ctx, adapterproto.MethodSessionCreate, r.createParams(1, "sess-1"), &raw)
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("native creation never began")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create returned %v", err)
	}
	waitFor(t, "cancelled creation response", func() bool {
		for _, line := range r.log.snapshot() {
			var response struct {
				Error *peer.Error `json:"error"`
			}
			if !line.toAdapter && json.Unmarshal(line.line, &response) == nil && response.Error != nil && response.Error.Code == peer.CodeRequestCancelled {
				return true
			}
		}
		return false
	})
	if len(r.ledgerLog().Kind(ledger.KindRuntimeSessionCreated)) != 1 || len(r.ledgerLog().Kind(ledger.KindNativeCreateReturned)) != 0 {
		t.Fatal("cancellation changed the native effect or reported a definite result")
	}
}
