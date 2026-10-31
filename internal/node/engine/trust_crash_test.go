package engine_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// The removal of a node from this node's trust writes the owner's entry, which is the
// commit point, the revocation of the credential the node holds, and the result.
// Wherever the node dies, the next start finds nothing, the entry alone, or the entry
// and the revocation without the result, or all three; never a revocation or a result
// without the entry. The same request sent again then finishes the removal, whichever
// of the writes are missing, and sent once more changes nothing.
func TestARemovalFromTrustRecoversFromACrashAtEveryPoint(t *testing.T) {
	noLeaks(t)
	const tx = "trust-local-revoke"
	peer := newPeerNode(t, 1)
	remove := func(l engine.LoopSide) error {
		_, err := l.RevokeNodeLocally(requestKey(0x71), fingerprintOf("remove"), "trust.revoke_node", peer.node)
		return err
	}
	var r *rig
	results := func(f *rig) (n int) {
		for _, a := range f.scan().Admin {
			if a.Key == requestKey(0x71) {
				n++
			}
		}
		return n
	}
	checked, none, entryOnly, whole := 0, 0, 0, 0
	walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
		checked++
		f := r.fork(disk)
		e, _, err := f.open()
		if err != nil {
			t.Errorf("%s: Open: %v", where, err)
			return
		}
		startForked(t, f, e)
		defer f.stop2(e)
		sc := f.scan()
		entry, revocations, stored := len(sc.LocalRevoked), len(sc.CredRevocations), results(f)
		if entry == 0 && (revocations != 0 || stored != 0) {
			t.Errorf("%s: %d revocations and %d results without the entry", where, revocations, stored)
		}
		switch {
		case entry == 0:
			none++
		case stored == 0:
			entryOnly++
		default:
			whole++
		}
		for round := 0; round < 2; round++ {
			if err := loopErr(t, e, remove); err != nil {
				t.Errorf("%s: the request sent again (%d): %v", where, round+1, err)
				return
			}
			sc = f.scan()
			if len(sc.LocalRevoked) != 1 || len(sc.CredRevocations) != 1 || results(f) != 1 {
				t.Errorf("%s: after the request again (%d): %d entries, %d revocations, %d results, want one of each",
					where, round+1, len(sc.LocalRevoked), len(sc.CredRevocations), results(f))
			}
		}
	}, tx)
	r = newRig(t, 10, withObserver(walker), withKeys())
	e := r.start()
	cred := peer.credentialWith(r.keys.IdentitySigner(), false)
	if err := loopErr(t, e, func(l engine.LoopSide) error { return l.InstallCredential(cred.Record, nil) }); err != nil {
		t.Fatal(err)
	}
	walker.rig = r
	if err := loopErr(t, e, remove); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if checked == 0 || none == 0 || entryOnly == 0 || whole == 0 {
		t.Fatalf("%d states judged: %d with nothing, %d with the entry alone, %d with all; the walk must reach every side of the commit", checked, none, entryOnly, whole)
	}
	var missing []string
	for _, k := range r.tab.Keys() {
		if k.Tag.Tx != tx {
			continue
		}
		for _, model := range crashKindNames {
			if walker.taken[k.Key][model] == 0 {
				missing = append(missing, fmt.Sprintf("%v under %s", k.Key, model))
			}
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("crash points never crashed at: %v", missing)
	}
}
