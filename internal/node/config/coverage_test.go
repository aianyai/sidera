package config_test

import (
	"bytes"
	"errors"
	"sort"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// The declarations of config form a valid table with the others, with the right
// kinds, and every role the package exports is a valid name.
func TestTransactionsAreValid(t *testing.T) {
	decls := append(txn.Internal(), config.Transactions()...)
	tab, err := txn.NewTable(decls...)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	kinds := map[string]txn.Kind{}
	for _, d := range config.Transactions() {
		kinds[d.Name] = d.Kind
	}
	want := map[string]txn.Kind{
		"node-set": txn.Normal, "presets-change": txn.Normal, "endpoint-set": txn.Normal,
		"adapter-enable": txn.Normal, "adapter-disable": txn.Normal,
		"adapter-stage": txn.Normal, "adapter-unstage": txn.Normal,
		"adapter-gc": txn.Recovery,
	}
	for name, kind := range want {
		if got, ok := kinds[name]; !ok || got != kind {
			t.Errorf("transaction %q: kind %v present %v, want %v", name, got, ok, kind)
		}
	}
	if len(kinds) != len(want) {
		t.Errorf("config declares %d transactions, the test knows %d: name the new one", len(kinds), len(want))
	}
	for _, k := range tab.Keys() {
		if err := k.Key.Validate(); err != nil {
			t.Errorf("key %v: %v", k.Key, err)
		}
	}
	for _, role := range []string{config.RoleDir, config.RoleNode, config.RolePolicy, config.RolePresets, config.RoleInstances,
		config.RoleEndpointIDs, config.RoleBackup, config.RoleRevocation, config.RoleStale, config.RoleVersion, config.RoleCurrent, config.RoleOld} {
		if !fsx.ValidName(role) {
			t.Errorf("role %q is not a valid name", role)
		}
	}
}

// Every crash point config declares is reached by the tests: a run of every
// transaction on one Writer leaves no key of config's table at zero. A write
// path that no test walks would be a crash point nobody ever stopped at.
func TestEveryDeclaredCrashPointIsReached(t *testing.T) {
	rg, c := loaded(t)
	open := isOpen(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ensureState(t, rg.mem)

	_, err := c.SetNodeSettings(request(1, 3), []byte(`{"reply_permission": false}`), result(new([]config.Node), `{}`))
	must(err)
	_, err = c.AddPreset(request(2, 2), "scratch", []byte(`{"local_sources": "deny"}`), result(new([]config.Presets), `{}`))
	must(err)
	_, err = c.EnableAdapter(request(3, 4), "second", "example", nil, open, result(new([]config.Instances), `{}`))
	must(err)
	_, err = c.SetEndpointSettings(request(4, 5), "second", mustEndpoint(t, ep4Text), []byte(`{"backlog_limit": 3}`), open, result(new([]config.Instances), `{}`))
	must(err)
	_, err = c.DisableAdapter(request(5, 6), "second", open, result(new([]config.Instances), `{}`))
	must(err)
	_, err = c.DisableAdapter(request(6, 7), "example", open, result(new([]config.Instances), `{}`))
	must(err)

	// A first installation that is refused after staging: parent, stage, subdir,
	// file and the unstage of the whole adapter directory.
	staged, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
	must(err)
	must(c.Unstage(staged))
	// An upgrade whose version directory was renamed in and did not commit: the
	// unstage of the version.
	staged, err = c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
	must(err)
	plan, err := c.PlanInstall(staged, nil)
	must(err)
	tx := rg.w.Begin(testAdapterInstall)
	must(plan.Promote(tx))
	tx.Close()
	must(c.Unstage(staged))

	// The repair: a start on a disk with garbage.
	mkdirAll(t, rg.mem, config.AdapterStageDir("example", "beef"))
	_, err = config.Load(rg.w)
	must(err)
	// The unstage of a stage directory alone.
	staged, err = c.StageAdapter(srcPackage(t, "example", "3.0.0", nil), bytes.NewReader([]byte{9, 9, 9, 9, 9, 9, 9, 9}))
	must(err)
	must(c.Unstage(staged))

	var zero []string
	declared := map[string]bool{}
	for _, d := range config.Transactions() {
		declared[d.Name] = true
	}
	for _, cnt := range rg.w.Coverage() {
		if declared[cnt.Key.Tag.Tx] && cnt.Hits == 0 {
			zero = append(zero, cnt.Key.Key.String())
		}
	}
	sort.Strings(zero)
	if len(zero) != 0 {
		t.Errorf("crash points of config that no test reached: %v", zero)
	}
}

// A write that fails before its commit point is an ordinary failure, and the
// configuration in memory and on disk is as it was. One that fails after it
// is fatal, and memory still does not take the change over: the caller exits
// and the next start reads what is on disk. Either way nothing is acknowledged.
func TestFailedWriteDoesNotChangeMemory(t *testing.T) {
	for _, r := range []struct {
		step  fsx.Step
		fatal bool
	}{
		{fsx.StepCreate, false}, {fsx.StepWrite, false}, {fsx.StepSync, false}, {fsx.StepRename, false}, {fsx.StepSyncDir, true},
	} {
		t.Run(r.step.String(), func(t *testing.T) {
			mem := fsxtest.NewMem()
			putTree(t, mem, validTree(t))
			faulty := fsxtest.NewFaulty(t, mem)
			w := txn.New(faulty, newTable(t), nil)
			recoverWriter(t, w)
			c, err := config.Load(w)
			if err != nil {
				t.Fatal(err)
			}
			faulty.Fail(fsxtest.Match{Tx: "node-set", Role: config.RoleNode, Step: r.step}, syscall.EIO)
			_, err = c.SetNodeSettings(request(1, 3), []byte(`{"reply_permission": false}`), result(new([]config.Node), `{}`))
			if err == nil {
				t.Fatal("the injected failure did not surface")
			}
			if txn.IsFatal(err) != r.fatal {
				t.Errorf("IsFatal = %v, want %v for a failure at %v", txn.IsFatal(err), r.fatal, r.step)
			}
			if !errors.Is(err, syscall.EIO) {
				t.Errorf("error %v does not wrap the system error", err)
			}
			if got := c.Node(); got.Version != 3 || !got.Settings.ReplyPermission {
				t.Errorf("memory took over a change that was not acknowledged: %+v", got)
			}
		})
	}
}

// A failure while a package is staged is an ordinary failure, nothing is
// installed, and what the failed staging wrote is gone or collected at the next
// start.
func TestFailedStageIsAnOrdinaryFailure(t *testing.T) {
	for _, m := range []fsxtest.Match{
		{Tx: "adapter-stage", Role: "parent", Step: fsx.StepMkdir},
		{Tx: "adapter-stage", Role: "stage", Step: fsx.StepMkdir},
		{Tx: "adapter-stage", Role: "subdir", Step: fsx.StepMkdir},
		{Tx: "adapter-stage", Role: "file", Step: fsx.StepWrite, Hit: 2},
		{Tx: "adapter-stage", Role: "file", Step: fsx.StepSync, Hit: 3},
		{Tx: "adapter-stage", Role: "file", Step: fsx.StepRename, Hit: 1},
	} {
		t.Run(m.Role+"."+m.Step.String(), func(t *testing.T) {
			mem := fsxtest.NewMem()
			putTree(t, mem, validTree(t))
			faulty := fsxtest.NewFaulty(t, mem)
			w := txn.New(faulty, newTable(t), nil)
			recoverWriter(t, w)
			c, err := config.Load(w)
			if err != nil {
				t.Fatal(err)
			}
			faulty.Fail(m, fsxtest.DiskFull)
			_, err = c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
			if err == nil {
				t.Fatal("the injected failure did not surface")
			}
			if txn.IsFatal(err) {
				t.Errorf("a failure while staging is fatal: %v", err)
			}
			if len(c.Adapters()) != 1 {
				t.Errorf("adapters = %+v", c.Adapters())
			}
			// Whatever is left is collected by the next start.
			c2, err := restart(t, mem.Clone())
			if err != nil || len(c2.Adapters()) != 1 {
				t.Errorf("the next start: %v", err)
			}
		})
	}
}
