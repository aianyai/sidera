package daemon_test

import (
	"context"
	"net"
	"reflect"
	"sort"
	"testing"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/txn"
)

// A hook is given nothing that writes the disk: the engine is the single writer of
// the data directory and one declaration has one open transaction, so a hook that
// held the transaction writer could break both. The keys and the configuration
// come as views whose methods are exactly the reads; the list is written out here,
// so a method that changes something is not added to a view unnoticed.
func TestAHookIsGivenNoWriterAndOnlyReadsOfTheKeysAndTheConfiguration(t *testing.T) {
	env := reflect.TypeOf(daemon.Env{})
	writer := reflect.TypeOf((*txn.Writer)(nil))
	for i := 0; i < env.NumField(); i++ {
		if f := env.Field(i); f.Type == writer {
			t.Errorf("Env.%s is the transaction writer", f.Name)
		}
	}
	methods := func(typ reflect.Type) []string {
		var out []string
		for i := 0; i < typ.NumMethod(); i++ {
			out = append(out, typ.Method(i).Name)
		}
		sort.Strings(out)
		return out
	}
	for _, tc := range []struct {
		field string
		want  []string
	}{
		{"Keys", []string{"Certificate", "CertificateWire", "NetworkSigner", "Node", "RenewalDue", "RenewalInterrupted", "SelfRevocation", "SerialSeen"}},
		{"Config", []string{"Adapters", "Backup", "EndpointIDs", "EndpointSettings", "Instances", "LastOperations", "Node", "Policy", "Presets", "RevocationSet", "Revocations"}},
	} {
		f, ok := env.FieldByName(tc.field)
		if !ok {
			t.Fatalf("Env has no field %s", tc.field)
		}
		if f.Type.Kind() != reflect.Interface {
			t.Errorf("Env.%s is %s, not a view", tc.field, f.Type)
			continue
		}
		if got := methods(f.Type); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("the methods of Env.%s are %v; want %v", tc.field, got, tc.want)
		}
	}
}

// What the Agent interface needs to listen on this home: the address, which comes
// from the data directory, and the assertion that the instance lock is held, which
// ipc.Listen asks for. A hook listens with them while the daemon holds the lock.
func TestAHookCanListenOnAnAddressOfThisHomeWithTheLockItIsGiven(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	var l net.Listener
	var listenErr error
	var heldInHook bool
	c.Attach.AgentProbeOnly = func(_ context.Context, env *daemon.Env) error {
		heldInHook = env.Lock != nil && env.Lock.Held()
		addr, err := ipc.Locate(env.Home, ipc.KindAgent)
		if err != nil {
			return err
		}
		l, listenErr = ipc.Listen(addr, env.Lock, ipc.Options{Logger: env.Log})
		return listenErr
	}
	c.Attach.StopInterfaces = func(context.Context) error {
		if l == nil {
			return nil
		}
		return l.Close()
	}
	started(t, c)
	if listenErr != nil || l == nil {
		t.Fatalf("the hook could not listen: %v", listenErr)
	}
	if !heldInHook {
		t.Error("the lock the hook was given did not say it was held")
	}
}
