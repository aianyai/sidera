package recipes_test

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
)

func TestTerminalJudgementRequiresOriginalDurabilityAndRealLifecycle(t *testing.T) {
	id := admin.OperationID{61}
	nodeBytes := make([]byte, ident.NodeIDLen)
	nodeBytes[0] = 1
	node, err := ident.NodeIDFromBytes(nodeBytes)
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"stopping":true}`)
	response, _ := json.Marshal(map[string]any{"operation_id": hex.EncodeToString(id[:]), "replayed": false, "result": result})
	replay, _ := json.Marshal(map[string]any{"operation_id": hex.EncodeToString(id[:]), "replayed": true, "result": result})
	query, _ := json.Marshal(map[string]any{"result": result})
	base := recipes.TerminalFacts{Node: node, RequestID: id, Response: response, Persisted: result, Exited: true, Audit: &recipes.TerminalAudit{Attempted: true, Started: true, Query: query, Replay: replay}}
	r := recipes.TerminalRecipe{Name: "stop", Kind: recipes.Stop, RequestID: id, MaxPoints: 6}
	for _, tc := range []struct {
		name   string
		change func(*recipes.TerminalFacts)
		valid  bool
	}{
		{"actual stop", func(*recipes.TerminalFacts) {}, true},
		{"confirmed without original durable result", func(f *recipes.TerminalFacts) { f.Persisted = nil }, false},
		{"no actual exit", func(f *recipes.TerminalFacts) { f.Exited = false }, false},
		{"audit replay cannot replace original result", func(f *recipes.TerminalFacts) { f.Persisted = nil; f.Response = nil }, false},
		{"missing replay", func(f *recipes.TerminalFacts) { f.Audit.Replay = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			a := *base.Audit
			f.Audit = &a
			tc.change(&f)
			if err := r.Check(f); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
	retire := recipes.TerminalRecipe{Name: "retire", Kind: recipes.Retire, RequestID: id, MaxPoints: 11}
	facts := recipes.TerminalFacts{Node: node, RequestID: id, Retired: true, RecoveryRejected: true, SigningUnchanged: true}
	if err := retire.Check(facts); err != nil {
		t.Fatal(err)
	}
	facts.Retired = false
	if err := retire.Check(facts); err == nil {
		t.Fatal("startup failure passed without valid retirement")
	}
}
