package config_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
)

// changes is one change of each versioned collection, as a function of the
// request: the node settings, the preset collection and the instance
// collection, each with its file and its version in the valid tree.
type change struct {
	name    string
	file    string
	current admin.Version
	do      func(c *config.Config, req config.Request, open config.OpenEndpoints) (admin.Version, *config.Operation, error)
}

func changes(t testing.TB) []change {
	ep4 := mustEndpoint(t, ep4Text)
	return []change{
		{"node set", config.NodePath, 3, func(c *config.Config, req config.Request, _ config.OpenEndpoints) (admin.Version, *config.Operation, error) {
			var seen []config.Node
			n, err := c.SetNodeSettings(req, []byte(`{"reply_permission": false}`), result(&seen, `{"changed":"node"}`))
			return n.Version, n.LastOperation, err
		}},
		{"preset add", config.PresetsPath, 2, func(c *config.Config, req config.Request, _ config.OpenEndpoints) (admin.Version, *config.Operation, error) {
			var seen []config.Presets
			p, err := c.AddPreset(req, "extra", []byte(`{"local_sources": "allow"}`), result(&seen, `{"changed":"presets"}`))
			return p.Version, p.LastOperation, err
		}},
		{"preset remove", config.PresetsPath, 2, func(c *config.Config, req config.Request, _ config.OpenEndpoints) (admin.Version, *config.Operation, error) {
			var seen []config.Presets
			p, err := c.RemovePreset(req, "sandboxed", result(&seen, `{"changed":"presets"}`))
			return p.Version, p.LastOperation, err
		}},
		{"adapter enable", config.InstancesPath, 4, func(c *config.Config, req config.Request, open config.OpenEndpoints) (admin.Version, *config.Operation, error) {
			var seen []config.Instances
			i, err := c.EnableAdapter(req, "second", "example", nil, open, result(&seen, `{"changed":"instances"}`))
			return i.Version, i.LastOperation, err
		}},
		{"adapter disable", config.InstancesPath, 4, func(c *config.Config, req config.Request, open config.OpenEndpoints) (admin.Version, *config.Operation, error) {
			var seen []config.Instances
			i, err := c.DisableAdapter(req, "example", open, result(&seen, `{"changed":"instances"}`))
			return i.Version, i.LastOperation, err
		}},
		{"endpoint set", config.InstancesPath, 4, func(c *config.Config, req config.Request, open config.OpenEndpoints) (admin.Version, *config.Operation, error) {
			var seen []config.Instances
			i, err := c.SetEndpointSettings(req, "example", ep4, []byte(`{"backlog_limit": 7}`), open, result(&seen, `{"changed":"instances"}`))
			return i.Version, i.LastOperation, err
		}},
	}
}

func isOpen(t testing.TB) config.OpenEndpoints {
	ep4 := mustEndpoint(t, ep4Text)
	return func(id ident.EndpointID) bool { return id == ep4 }
}

// A change based on a version that is not the current one is refused with the
// current version, for every collection, and writes nothing. The version
// is the one of the collection, not of the member: any member's change counts.
func TestExpectedVersionConflictPerCollection(t *testing.T) {
	for _, ch := range changes(t) {
		for _, expected := range []admin.Version{0, ch.current - 1, ch.current + 1, 99} {
			t.Run(fmt.Sprintf("%s/expected %d", ch.name, expected), func(t *testing.T) {
				rg, c := loaded(t)
				before := snapshot(t, rg.mem)
				_, _, err := ch.do(c, request(5, expected), isOpen(t))
				ce := changeReason(t, err)
				if ce.Reason != config.ChangeVersionConflict || ce.Current != ch.current {
					t.Errorf("error = %+v, want a version conflict at current %d", ce, ch.current)
				}
				if !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
					t.Error("a refused change wrote to the disk")
				}
				if got := rg.load(); !reflect.DeepEqual(got.Node(), c.Node()) || !reflect.DeepEqual(got.Presets(), c.Presets()) || !reflect.DeepEqual(got.Instances(), c.Instances()) {
					t.Error("a refused change altered the configuration in memory")
				}
			})
		}
	}
}

// A change based on the current version succeeds: the version goes up by one,
// the operation and its result are recorded in the file it changed, in the same
// replacement, and what is in memory is what is on disk.
func TestVersionedChangeRecordsTheOperation(t *testing.T) {
	for _, ch := range changes(t) {
		t.Run(ch.name, func(t *testing.T) {
			rg, c := loaded(t)
			req := request(6, ch.current)
			version, op, err := ch.do(c, req, isOpen(t))
			if err != nil {
				t.Fatalf("change: %v", err)
			}
			if version != ch.current+1 {
				t.Errorf("version = %d, want %d", version, ch.current+1)
			}
			if op == nil || op.ID != req.ID || op.Fingerprint != req.Fingerprint || !op.Person.IsZero() || len(op.Result) == 0 {
				t.Fatalf("last operation = %+v, want the request's", op)
			}
			var res map[string]string
			if err := json.Unmarshal(op.Result, &res); err != nil || len(res["changed"]) == 0 {
				t.Errorf("result = %s, want the one the result function built", op.Result)
			}
			if op.Key() != (admin.RequestKey{ID: req.ID}) {
				t.Errorf("key = %+v", op.Key())
			}

			// The file holds the same operation and version, and a restart reads
			// the same configuration.
			data, err := rg.mem.ReadFile(ch.file)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Version       uint64 `json:"version"`
				LastOperation struct {
					ID          string `json:"id"`
					Fingerprint string `json:"fingerprint"`
				} `json:"last_operation"`
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Version != uint64(ch.current+1) || doc.LastOperation.ID != hexOfID(req.ID) {
				t.Errorf("the file holds version %d and operation %q", doc.Version, doc.LastOperation.ID)
			}
			again := rg.load()
			if !reflect.DeepEqual(again.Node(), c.Node()) || !reflect.DeepEqual(again.Presets(), c.Presets()) || !reflect.DeepEqual(again.Instances(), c.Instances()) {
				t.Error("a restart does not read what the change left in memory")
			}
			ops := c.LastOperations()
			found := false
			for _, o := range ops {
				found = found || o.Key() == op.Key()
			}
			if !found {
				t.Errorf("last operations %v lack the new one", ops)
			}
		})
	}
}

func hexOfID(id admin.OperationID) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 32)
	for _, b := range id {
		out = append(out, digits[b>>4], digits[b&15])
	}
	return string(out)
}

// The version is checked before the content: a request that is both stale and
// invalid is a conflict, so the sender refreshes before it fixes anything.
func TestStaleVersionIsReportedBeforeAnInvalidValue(t *testing.T) {
	_, c := loaded(t)
	var seen []config.Node
	_, err := c.SetNodeSettings(request(1, 1), []byte(`{"node_queue_limit": 0}`), result(&seen, `{}`))
	if ce := changeReason(t, err); ce.Reason != config.ChangeVersionConflict {
		t.Errorf("reason = %v, want a version conflict", ce.Reason)
	}
}

// Two changes in a row each need the version the earlier one produced.
func TestVersionAdvancesByOnePerChange(t *testing.T) {
	_, c := loaded(t)
	var seen []config.Node
	for i, patch := range []string{`{"reply_permission": false}`, `{"reply_permission": true}`, `{"node_prompt_turns": 4}`} {
		n, err := c.SetNodeSettings(request(byte(i+1), admin.Version(3+i)), []byte(patch), result(&seen, `{}`))
		if err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
		if n.Version != admin.Version(4+i) {
			t.Errorf("change %d: version %d, want %d", i, n.Version, 4+i)
		}
	}
	if len(seen) != 3 || seen[2].Version != 6 {
		t.Errorf("result functions saw %d states, the last at version %v", len(seen), seen[len(seen)-1].Version)
	}
}
