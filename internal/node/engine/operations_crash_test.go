package engine_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	nodekeys "github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
)

// The owner's operations on identity, alerts and settings are each walked through
// every crash point of their transaction, under every kind of crash. Wherever the
// node dies the next start finds nothing done, the effect without its result (for
// an operation whose result is kept apart from its effect), or both; never the
// result without the effect. The request sent again then runs afresh in the first
// case, finds its effect there in the second and says what that is, and is
// answered from the result in the third; it is never refused, and the identifier
// is never found to be reused by its own request.
func TestTheOwnersOperationsRecoverFromACrashAtEveryPoint(t *testing.T) {
	type op struct {
		name string
		txs  []string
		// setup runs on the rig before the walk: the pause on disk, the alert.
		setup func(r *rig)
		// cmd is the request; it is made again for each state.
		cmd func(f *rig) engine.ManagementCommand
		// first is the result of the request on a node that has not done it; again is
		// the result when the effect is on disk and the result is not. Empty again: the
		// effect and the result are one replacement, so that state does not exist.
		first, again func(f *rig) string
		// effect says whether the effect is on disk in the forked node.
		effect func(f *rig, sc *store.Scan) bool
		// inConfig: the result is kept in a versioned file of config/ and not under
		// state/admin/.
		inConfig bool
	}
	pausedOnDisk := func(r *rig) {
		if err := r.keys.SaveClock(nodekeys.ClockState{HighWater: t0, RollbackPausedSince: t0}); err != nil {
			t.Fatal(err)
		}
		r.paused = true
	}
	clearResult := func(cleared bool) func(*rig) string {
		return func(f *rig) string {
			now := f.clk.Now()
			b, err := encodeClear(engine.ClockClear{Cleared: cleared, HighWater: now, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
	alert := store.AlertRecord{ID: store.AlertID{0xa1}, Kind: "disk_low", At: t0, Detail: "d", Subject: "state"}
	for _, tc := range []op{
		{
			name: "clearing the pause of the clock", txs: []string{"clock-clear"},
			setup:  func(r *rig) { pausedOnDisk(r) },
			cmd:    func(*rig) engine.ManagementCommand { return clearCommand(111, "clear") },
			first:  clearResult(true),
			again:  clearResult(false),
			effect: func(f *rig, _ *store.Scan) bool { st, _ := nodekeys.ReadClock(f.mem); return !st.Paused() },
		},
		{
			name: "confirming the backup", txs: []string{"backup-confirm"},
			cmd: func(f *rig) engine.ManagementCommand {
				return backupCommand(112, "confirm", f.keys.SelfRevocation().Digest())
			},
			first:  func(*rig) string { return `{"confirmed_at":"` + t0.Format(time.RFC3339) + `"}` },
			again:  func(*rig) string { return `{"confirmed_at":"` + t0.Format(time.RFC3339) + `"}` },
			effect: func(f *rig, _ *store.Scan) bool { _, ok := f.cfg.Backup(); return ok },
		},
		{
			name: "retiring the identity", txs: []string{"identity-retire"},
			cmd:    func(f *rig) engine.ManagementCommand { return retireCommand(113, "retire", f.node) },
			first:  func(f *rig) string { return `{"node":"` + f.node.String() + `","retired":true,"stopping":true}` },
			again:  func(f *rig) string { return `{"node":"` + f.node.String() + `","retired":true,"stopping":true}` },
			effect: func(f *rig, _ *store.Scan) bool { return f.cfg.RevocationSet().NodeRetired(f.node) },
		},
		{
			name: "acknowledging an alert", txs: []string{"alert-ack"},
			setup:  func(r *rig) { r.seedRecord(alert) },
			cmd:    func(*rig) engine.ManagementCommand { return ackCommand(114, "ack", alert.ID) },
			first:  func(*rig) string { return `{"alert":"` + alert.ID.String() + `","acknowledged":true}` },
			again:  func(*rig) string { return `{"alert":"` + alert.ID.String() + `","acknowledged":true}` },
			effect: func(_ *rig, sc *store.Scan) bool { return len(sc.Alerts) == 1 && sc.Alerts[0].Acked != nil },
		},
		{
			name: "changing the settings of the node", txs: []string{"node-set"}, inConfig: true,
			cmd: func(f *rig) engine.ManagementCommand {
				return setNodeSettingsCommand(115, "node", 1, `{"node_queue_limit":12345}`)
			},
			first:  func(*rig) string { return "" },
			effect: func(f *rig, _ *store.Scan) bool { return f.cfg.Node().Settings.NodeQueueLimit == 12345 },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			var r *rig
			checked, stored, apart, none := 0, 0, 0, 0
			walker := newCrashWalker(37, func(where string, disk *fsxtest.Mem) {
				checked++
				f := r.fork(disk)
				if st, err := nodekeys.ReadClock(disk); err == nil && st.Paused() {
					f.paused = true
				}
				e, _, err := f.open()
				if err != nil {
					t.Errorf("%s: Open: %v", where, err)
					return
				}
				startForked(t, f, e)
				defer f.stop2(e)
				cmd := tc.cmd(f)
				sc := f.scan()
				var haveResult bool
				if tc.inConfig {
					for _, o := range f.cfg.LastOperations() {
						if o.Key() == cmd.Key {
							haveResult = true
						}
					}
				} else {
					_, haveResult = f.adminResultOf(sc, cmd)
				}
				effect := tc.effect(f, sc)
				if haveResult && !effect {
					t.Errorf("%s: the result is stored without its effect", where)
				}
				replayed := haveResult
				switch {
				case haveResult:
					stored++
				case effect:
					apart++
				default:
					none++
				}
				got, err := e.Execute(context.Background(), cmd)
				if err != nil {
					t.Errorf("%s: the request sent again: %v", where, err)
					return
				}
				if got.Replayed != replayed {
					t.Errorf("%s: replayed %v, want %v", where, got.Replayed, replayed)
				}
				if !tc.inConfig {
					want := tc.first(f)
					if effect && !haveResult {
						want = tc.again(f)
					}
					if string(got.Result) != want {
						t.Errorf("%s: answered %s, want %s", where, got.Result, want)
					}
				}
				if !tc.effect(f, f.scan()) {
					t.Errorf("%s: the effect is not there after the request was sent again", where)
				}
				second, err := e.Execute(context.Background(), cmd)
				if err != nil || !second.Replayed || string(second.Result) != string(got.Result) {
					t.Errorf("%s: the second resend: %+v, %v; want the result of the first", where, second, err)
				}
			}, tc.txs...)
			r = newRig(t, 10, withObserver(walker), withKeys())
			if tc.setup != nil {
				tc.setup(r)
			}
			e := r.start()
			walker.rig = r
			if _, err := e.Execute(context.Background(), tc.cmd(r)); err != nil {
				t.Fatal(err)
			}
			settle(t, e)
			if checked == 0 || stored == 0 || none == 0 || (tc.again != nil && apart == 0) {
				t.Fatalf("%d states judged: %d with the result, %d with the effect alone, %d with neither; the walk must reach every side of the commit", checked, stored, apart, none)
			}
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
			if r.commits(tc.txs[0]) < 1 && !tc.inConfig {
				t.Errorf("transaction %s never reached its commit", tc.txs[0])
			}
		})
	}
}
