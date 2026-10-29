//go:build crashtest

package daemon_test

import (
	"encoding/json"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/txn"
	"testing"
)

func TestTaggedManagementQueryUsesTheCompleteDaemonTable(t *testing.T) {
	cfg, _ := initialized(t)
	started(t, cfg)
	status, raw := manage(t, cfg.Home, "test.crashpoints", map[string]any{})
	var result struct {
		Result struct{ Points []struct{ Key, Kind string } }
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	table, err := daemon.Table()
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 || len(result.Result.Points) != len(table.Keys()) {
		t.Fatalf("query: %d %s", status, raw)
	}
	for i, k := range table.Keys() {
		kind := "normal"
		if k.Kind == txn.Recovery {
			kind = "recovery"
		}
		if result.Result.Points[i].Key != k.Key.String() || result.Result.Points[i].Kind != kind {
			t.Fatalf("point %d: %+v", i, result.Result.Points[i])
		}
	}
}
