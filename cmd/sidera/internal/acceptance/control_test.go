package acceptance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
)

func TestAdapterAcquisitionPreservesCallerError(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "canceled"
		want := context.Canceled
		if deadline {
			name, want = "deadline", context.DeadlineExceeded
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			server, err := fakectl.Listen(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			client, err := fakectl.Dial(ctx, server.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			client.HandleClock(func(int64) {})
			if err := client.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: "cached", PID: 1}); err != nil {
				t.Fatal(err)
			}
			var caller context.Context
			var cancel context.CancelFunc
			if deadline {
				caller, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
			} else {
				caller, cancel = context.WithCancel(ctx)
				cancel()
			}
			defer cancel()
			ended := make(chan struct{})
			close(ended)
			f := &Fixture{control: server}
			for _, generation := range []*generation{nil, {p: &process{done: ended}}} {
				f.current = generation
				ctl, err := f.Adapter(caller, "cached")
				if ctl != nil || !errors.Is(err, want) {
					t.Fatalf("controller=%v error=%v, want nil and %v", ctl, err, want)
				}
			}
		})
	}
}

func TestAdapterAcquisitionAllowsSameGenerationReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	server, err := fakectl.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	connect := func() (*fakectl.Client, <-chan int64) {
		t.Helper()
		client, err := fakectl.Dial(ctx, server.Addr())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		clock := make(chan int64, 1)
		client.HandleClock(func(ms int64) { clock <- ms })
		if err := client.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: "reconnecting", PID: 1}); err != nil {
			t.Fatal(err)
		}
		return client, clock
	}
	first, _ := connect()
	conn, err := server.Wait(ctx, "reconnecting")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	<-conn.Done()
	f := &Fixture{control: server, current: &generation{p: &process{done: make(chan struct{})}}, now: time.Unix(1800000000, 0)}
	result := make(chan error, 1)
	go func() {
		ctl, err := f.Adapter(ctx, "reconnecting")
		if ctl == nil && err == nil {
			err = errors.New("reconnection returned no controller")
		}
		result <- err
	}()
	_, clock := connect()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if got := <-clock; got != f.Now().UnixMilli() {
		t.Fatalf("replacement clock=%d, want %d", got, f.Now().UnixMilli())
	}
}
