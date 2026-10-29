//go:build crashtest

package manageapi

import (
	"encoding/json"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

func TestCrashPointQueryKeepsKindsAndSyncPoints(t *testing.T) {
	keys := []txn.Key{{Key: fsx.Key{Tag: fsx.Tag{Tx: "sample", Role: "file"}, Step: fsx.StepSync}, Kind: txn.Recovery}}
	r := newRig(t, func(s *Server) { s.cfg.CrashPoints = keys })
	status, out := r.post("test.crashpoints", `{}`)
	var result struct {
		Result struct{ Points []struct{ Key, Kind string } }
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if status != 200 || len(result.Result.Points) != 1 || result.Result.Points[0].Key != keys[0].Key.String() || result.Result.Points[0].Kind != "recovery" {
		t.Fatalf("query: %d %s", status, out)
	}
}
