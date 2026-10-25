package engine_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// An administrative operation and its result are written by one transaction, the
// result last, so wherever the node dies the next start finds one of three things:
// nothing, the effect without its result (the transaction commits with its first
// write), or both; never the result without the effect. The request sent again is
// then carried out afresh in the first case, found already done in the second
// (closing an endpoint that is closed says so) and answered from the result in
// the third; it is never an error, and the identifier is never found to be reused
// by its own request. Each operation of the door is walked through every crash
// point of its transaction, under every kind of crash.
func TestAnAdministrativeOperationRecoversFromACrashAtEveryPoint(t *testing.T) {
	type op struct {
		name string
		txs  []string
		// first is the result of the operation when it is carried out on a node
		// that has not done it, and again when the effect is there and the result is
		// not
		first, again string
		cmd          func(a ident.EndpointID) engine.ManagementCommand
		// effect says whether the effect is on disk in a scan
		effect func(sc *store.Scan, a ident.EndpointID) bool
	}
	for _, tc := range []op{
		{
			name: "closing an endpoint", txs: []string{"endpoint-close", "endpoint-drain"},
			first: `{"outcome":"closed"}`, again: `{"outcome":"already_closed"}`,
			cmd: func(a ident.EndpointID) engine.ManagementCommand { return closeCommand(61, "close a", a) },
			effect: func(sc *store.Scan, a ident.EndpointID) bool {
				for _, ep := range sc.Endpoints {
					if ep.ID == a {
						return ep.Status == store.EndpointClosed
					}
				}
				return false
			},
		},
		{
			name: "stopping the daemon", txs: []string{"daemon-stop"},
			first: `{"stopping":true}`, again: `{"stopping":true}`,
			cmd:    func(ident.EndpointID) engine.ManagementCommand { return stopCommand(62, "stop") },
			effect: func(*store.Scan, ident.EndpointID) bool { return false }, // its effect is its result
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			var r *rig
			var a ident.EndpointID
			checked, stored, apart, none := 0, 0, 0, 0
			walker := newCrashWalker(31, func(where string, disk *fsxtest.Mem) {
				checked++
				f := r.fork(disk)
				e, _, err := f.open()
				if err != nil {
					t.Errorf("%s: Open: %v", where, err)
					return
				}
				startForked(t, f, e)
				defer f.stop2(e)
				cmd := tc.cmd(a)
				sc := f.scan()
				_, haveResult := f.adminResultOf(sc, cmd)
				effect := tc.effect(sc, a)
				want, wantReplayed := tc.first, false
				switch {
				case haveResult:
					stored++
					want, wantReplayed = tc.first, true
					if tc.name == "closing an endpoint" && !effect {
						t.Errorf("%s: the result is stored without its effect", where)
					}
				case effect:
					apart++
					want = tc.again
				default:
					none++
				}
				got, err := e.Execute(context.Background(), cmd)
				if err != nil {
					t.Errorf("%s: the request sent again: %v", where, err)
					return
				}
				if string(got.Result) != want || got.Replayed != wantReplayed {
					t.Errorf("%s: answered %s (replayed %v), want %s (replayed %v)", where, got.Result, got.Replayed, want, wantReplayed)
				}
				again, err := e.Execute(context.Background(), cmd)
				if err != nil || !again.Replayed || string(again.Result) != want {
					t.Errorf("%s: the second resend: %+v, %v; want the result stored by the first", where, again, err)
				}
			}, tc.txs...)
			r = newRig(t, 10, withObserver(walker))
			e := r.start()
			a = register(t, e, "a")
			r.restart()
			e = r.start()
			walker.rig = r
			if _, err := e.Execute(context.Background(), tc.cmd(a)); err != nil {
				t.Fatal(err)
			}
			settle(t, e)
			if checked == 0 || stored == 0 || none == 0 || (tc.name == "closing an endpoint" && apart == 0) {
				t.Fatalf("%d states judged: %d with the result, %d with the effect alone, %d with neither; the walk must reach every side of the commit", checked, stored, apart, none)
			}
			// every key of the transaction's declaration that the run reached was
			// crashed at under every kind of crash
			var missing []string
			for _, k := range r.tab.Keys() {
				if k.Tag.Tx != tc.txs[0] {
					continue
				}
				reached := false
				for _, c := range r.w.Coverage() {
					if c.Key.Key == k.Key && c.Hits > 0 {
						reached = true
					}
				}
				if !reached {
					continue
				}
				for _, model := range []string{"process crash", "worst power loss", "power loss"} {
					if walker.taken[k.Key][model] == 0 {
						missing = append(missing, fmt.Sprintf("%v under %s", k.Key, model))
					}
				}
			}
			sort.Strings(missing)
			if len(missing) != 0 {
				t.Errorf("crash points reached and never crashed at: %v", missing)
			}
			if r.commits(tc.txs[0]) < 1 {
				t.Errorf("transaction %s never reached its commit", tc.txs[0])
			}
		})
	}
}

// adminResultOf is the result stored under the key of the command, in a scan.
func (r *rig) adminResultOf(sc *store.Scan, c engine.ManagementCommand) (store.AdminRecord, bool) {
	for _, a := range sc.Admin {
		if a.Key == c.Key {
			return a, true
		}
	}
	return store.AdminRecord{}, false
}
