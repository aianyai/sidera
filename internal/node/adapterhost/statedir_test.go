package adapterhost_test

import (
	"crypto/rand"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The adapter is told the directory of its instance in initialize, and the
// directory is there, owner-only, before the adapter is started: it is made by
// the host, not left to the adapter, and it is the one the launcher is given too.
func TestTheStateDirectoryExistsPrivateAndIsSentInInitialize(t *testing.T) {
	r := newRig(t, rigOptions{})
	var (
		mu     sync.Mutex
		sent   adapterproto.InstanceInfo
		seen   fsx.Info
		seenOK bool
	)
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, _ := r.scriptedProcess(func(a *scripted) {
			a.initialize = func(p adapterproto.InitializeParams) (any, error) {
				mu.Lock()
				defer mu.Unlock()
				sent = p.Instance
				if fs, err := fsx.OpenOS(r.home, rand.Reader); err == nil {
					seen, err = fs.Stat("adapter-state/chat")
					seenOK = err == nil
				}
				return a.initializeResult(), nil
			}
		}, false)
		return pp, nil
	}
	r.start()
	r.waitConnected("chat")
	want := adapterhost.StateDir(r.home, "chat")
	mu.Lock()
	defer mu.Unlock()
	if sent.StateDir != want {
		t.Fatalf("initialize carried state_dir %q, want %q", sent.StateDir, want)
	}
	if !seenOK || !seen.IsDir || !seen.OwnerOnly {
		t.Fatalf("when initialize arrived the state directory was %+v (found: %v), want an owner-only directory", seen, seenOK)
	}
}
