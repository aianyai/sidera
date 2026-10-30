package fakeadapter_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type inputReceiver struct {
	mu     sync.Mutex
	values []fakeadapter.ExternalInputs
	apply  func(context.Context) error
}

func (r *inputReceiver) ApplyInputs(ctx context.Context, in fakeadapter.ExternalInputs) error {
	if r.apply != nil {
		if err := r.apply(ctx); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, in)
	return nil
}
func (r *inputReceiver) last() fakeadapter.ExternalInputs {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[len(r.values)-1]
}
func declaredInputs() fakeadapter.ExternalInputs {
	return fakeadapter.ExternalInputs{PromptRefusal: adapterproto.RefusalRuntimeAuth, Availability: []adapterproto.Availability{{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: testStart.UnixMilli(), Endpoints: []adapterproto.EndpointRef{"sess-1"}}}}
}

func TestInputRegistryRetainsOnlyExplicitDesiredInputs(t *testing.T) {
	for _, name := range []string{"initial", "set before first attach", "online replace", "two reconnects", "clear", "isolation", "failed apply retains desired"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			value := declaredInputs()
			initial := map[string]fakeadapter.ExternalInputs{}
			if name == "initial" {
				initial["a"] = value
			}
			owner, err := fakeadapter.NewInputRegistry([]string{"a", "b"}, initial)
			if err != nil {
				t.Fatal(err)
			}
			if name != "initial" {
				if err := owner.Set(ctx, "a", value); err != nil {
					t.Fatal(err)
				}
			}
			receiver := &inputReceiver{}
			detach, err := owner.Attach(ctx, "a", receiver)
			if err != nil {
				t.Fatal(err)
			}
			defer detach()
			want := value
			switch name {
			case "online replace":
				want = fakeadapter.ExternalInputs{PromptRefusal: adapterproto.RefusalBusy}
				if err := owner.Set(ctx, "a", want); err != nil {
					t.Fatal(err)
				}
			case "two reconnects":
				for range 2 {
					detach()
					receiver = &inputReceiver{}
					detach, err = owner.Attach(ctx, "a", receiver)
					if err != nil {
						t.Fatal(err)
					}
					defer detach()
				}
			case "clear":
				want = fakeadapter.ExternalInputs{}
				if err := owner.Set(ctx, "a", want); err != nil {
					t.Fatal(err)
				}
				detach()
				receiver = &inputReceiver{}
				detach, err = owner.Attach(ctx, "a", receiver)
				if err != nil {
					t.Fatal(err)
				}
				defer detach()
			case "isolation":
				other := &inputReceiver{}
				d, err := owner.Attach(ctx, "b", other)
				if err != nil {
					t.Fatal(err)
				}
				defer d()
				if !reflect.DeepEqual(other.last(), fakeadapter.ExternalInputs{}) {
					t.Fatal("another instance inherited inputs")
				}
			case "failed apply retains desired":
				receiver.apply = func(context.Context) error { return errors.New("offline") }
				want = fakeadapter.ExternalInputs{PromptRefusal: adapterproto.RefusalBusy}
				if err := owner.Set(ctx, "a", want); err == nil {
					t.Fatal("failed apply returned success")
				}
				detach()
				receiver = &inputReceiver{}
				detach, err = owner.Attach(ctx, "a", receiver)
				if err != nil {
					t.Fatal(err)
				}
				defer detach()
			}
			if !reflect.DeepEqual(receiver.last(), want) {
				t.Fatalf("installed %+v, want %+v", receiver.last(), want)
			}
		})
	}
}

func TestInputRegistrySerializesAttachAndSetAndIgnoresOldDetach(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	owner, err := fakeadapter.NewInputRegistry([]string{"a", "b"}, map[string]fakeadapter.ExternalInputs{"a": declaredInputs()})
	if err != nil {
		t.Fatal(err)
	}
	old := &inputReceiver{}
	oldDetach, err := owner.Attach(ctx, "a", old)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var installations int
	replacement := &inputReceiver{apply: func(ctx context.Context) error {
		installations++
		if installations > 1 {
			return nil
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	attached := make(chan error, 1)
	var detach func()
	go func() { var err error; detach, err = owner.Attach(ctx, "a", replacement); attached <- err }()
	<-entered
	wanted := fakeadapter.ExternalInputs{PromptRefusal: adapterproto.RefusalBusy}
	set := make(chan error, 1)
	go func() { set <- owner.Set(ctx, "a", wanted) }()
	// A different instance remains usable while this one's installation waits.
	if err := owner.Set(ctx, "b", wanted); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-attached; err != nil {
		t.Fatal(err)
	}
	defer detach()
	if err := <-set; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replacement.last(), wanted) {
		t.Fatal("Attach overwrote a concurrent Set")
	}
	oldDetach()
	if err := owner.Set(ctx, "a", fakeadapter.ExternalInputs{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replacement.last(), fakeadapter.ExternalInputs{}) {
		t.Fatal("old detach removed the replacement")
	}
}

func TestInputRegistryValidationCopiesAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	original := declaredInputs()
	initial := map[string]fakeadapter.ExternalInputs{"a": original}
	owner, err := fakeadapter.NewInputRegistry([]string{"a"}, initial)
	if err != nil {
		t.Fatal(err)
	}
	original.Availability[0].Endpoints[0] = "mutated"
	receiver := &inputReceiver{}
	detach, err := owner.Attach(ctx, "a", receiver)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	if receiver.last().Availability[0].Endpoints[0] != "sess-1" {
		t.Fatal("constructor retained caller's nested slice")
	}
	// Receivers own their installed copy as well.
	receiver.last().Availability[0].Endpoints[0] = "receiver-mutated"
	receiver2 := &inputReceiver{}
	d, err := owner.Attach(ctx, "a", receiver2)
	if err != nil {
		t.Fatal(err)
	}
	defer d()
	if receiver2.last().Availability[0].Endpoints[0] != "sess-1" {
		t.Fatal("receiver mutated desired state")
	}
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		instance string
		value    fakeadapter.ExternalInputs
	}{
		{"unknown instance", ctx, "unknown", fakeadapter.ExternalInputs{}},
		{"invalid refusal", ctx, "a", fakeadapter.ExternalInputs{PromptRefusal: "invalid"}},
		{"invalid availability", ctx, "a", fakeadapter.ExternalInputs{Availability: []adapterproto.Availability{{Condition: "invalid"}}}},
		{"cancelled", func() context.Context { c, cancel := context.WithCancel(ctx); cancel(); return c }(), "a", fakeadapter.ExternalInputs{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := owner.Set(tc.ctx, tc.instance, tc.value); err == nil {
				t.Fatal("invalid Set succeeded")
			}
		})
	}
	final := &inputReceiver{}
	d3, err := owner.Attach(ctx, "a", final)
	if err != nil {
		t.Fatal(err)
	}
	defer d3()
	if final.last().Availability[0].Endpoints[0] != "sess-1" || final.last().PromptRefusal != adapterproto.RefusalRuntimeAuth {
		t.Fatal("rejected Set changed desired state")
	}
}

func TestInputRegistryCancelledAttachDoesNotPublishOrBlockCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	owner, err := fakeadapter.NewInputRegistry([]string{"a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	live := &inputReceiver{}
	d, err := owner.Attach(ctx, "a", live)
	if err != nil {
		t.Fatal(err)
	}
	defer d()
	installCtx, stop := context.WithCancel(ctx)
	entered := make(chan struct{})
	bad := &inputReceiver{apply: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	failed := make(chan error, 1)
	go func() { _, err := owner.Attach(installCtx, "a", bad); failed <- err }()
	<-entered
	// A Set queued behind the installation can cancel without changing desired.
	waiting, stopWaiting := context.WithCancel(ctx)
	stopWaiting()
	if err := owner.Set(waiting, "a", declaredInputs()); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued Set: %v", err)
	}
	stop()
	if err := <-failed; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Attach: %v", err)
	}
	value := declaredInputs()
	if err := owner.Set(ctx, "a", value); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live.last(), value) {
		t.Fatal("failed Attach replaced the live binding")
	}
}
