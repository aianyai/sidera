package recipes

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// TerminalKind describes an operation whose successful completion ends service.
type TerminalKind uint8

const (
	Stop TerminalKind = iota + 1
	Retire
)

// TerminalRecipe owns the domain judgement for a real terminal lifecycle.
// Bindings execute its request and supply actual facts, without scenario.Env.
type TerminalRecipe struct {
	Needs     Needs
	Name      string
	Kind      TerminalKind
	RequestID admin.OperationID
	MaxPoints int
}

// TerminalAudit records an additional verification attempt after the walk chain
// is terminal. Trace is independent evidence, never a coverage source.
type TerminalAudit struct {
	Attempted, Started, IdentityRetired, SigningUnchanged bool
	Trace                                                 []crashwalk.Hit
	StartError, ExitError                                 string
	Query, Replay                                         json.RawMessage
}

// TerminalFacts preserves the response and stopped-disk result before the final
// audit can change anything. Missing responses and results remain absent.
type TerminalFacts struct {
	Node                                                ident.NodeID
	RequestID                                           admin.OperationID
	Response, Persisted                                 json.RawMessage
	Exited, Retired, RecoveryRejected, SigningUnchanged bool
	Audit                                               *TerminalAudit
}

func terminalRecipes() []TerminalRecipe {
	return []TerminalRecipe{
		{Name: "management-stop", Kind: Stop, RequestID: admin.OperationID{61}, MaxPoints: 6},
		{Name: "management-retire", Kind: Retire, RequestID: admin.OperationID{62}, MaxPoints: 11},
	}
}

// Check judges only supplied observations; it never invents a response from an
// on-disk effect or treats an arbitrary failed startup as successful recovery.
func (r TerminalRecipe) Check(f TerminalFacts) error {
	fail := func(reason string) error { return fmt.Errorf("%w: %s: %s", ErrManagementEffect, r.Name, reason) }
	if f.Node == (ident.NodeID{}) || f.RequestID != r.RequestID {
		return fail("wrong node or request identifier")
	}
	var response json.RawMessage
	if len(f.Response) != 0 {
		var envelope struct {
			OperationID string          `json:"operation_id"`
			Replayed    bool            `json:"replayed"`
			Result      json.RawMessage `json:"result"`
		}
		if err := strictjson.Decode(f.Response, &envelope); err != nil || envelope.OperationID != hex.EncodeToString(r.RequestID[:]) {
			return fail("invalid actual response")
		}
		response = envelope.Result
		if len(f.Persisted) == 0 || !bytes.Equal(response, f.Persisted) {
			return fail("confirmed response has no matching original durable result")
		}
	}
	if len(f.Persisted) != 0 {
		if r.Kind == Stop {
			var result struct {
				Stopping bool `json:"stopping"`
			}
			if err := strictjson.Decode(f.Persisted, &result); err != nil || !result.Stopping {
				return fail("invalid persisted stop result")
			}
		} else if r.Kind == Retire {
			var result struct {
				Node     string `json:"node"`
				Retired  bool   `json:"retired"`
				Stopping bool   `json:"stopping"`
			}
			if err := strictjson.Decode(f.Persisted, &result); err != nil || result.Node != f.Node.String() || !result.Retired || !result.Stopping {
				return fail("invalid persisted retirement result")
			}
		}
	}
	switch r.Kind {
	case Stop:
		if !f.Exited || f.Retired || f.RecoveryRejected || len(response) == 0 || len(f.Persisted) == 0 {
			return fail("stop did not complete its real request and exit")
		}
		a := f.Audit
		if a == nil || !a.Attempted || !a.Started || a.IdentityRetired || a.StartError != "" || a.ExitError != "" {
			return fail("stop did not restart successfully")
		}
		var query struct {
			Result json.RawMessage `json:"result"`
		}
		if err := strictjson.Decode(a.Query, &query); err != nil || !bytes.Equal(query.Result, f.Persisted) {
			return fail("restarted result lookup changed the original result")
		}
		var replay struct {
			OperationID string          `json:"operation_id"`
			Replayed    bool            `json:"replayed"`
			Result      json.RawMessage `json:"result"`
		}
		if err := strictjson.Decode(a.Replay, &replay); err != nil || replay.OperationID != hex.EncodeToString(r.RequestID[:]) || !replay.Replayed || !bytes.Equal(replay.Result, f.Persisted) {
			return fail("restarted same-request replay changed the result")
		}
	case Retire:
		if !f.Retired {
			return fail("no verified node-bound retirement")
		}
		if f.RecoveryRejected {
			if !f.SigningUnchanged || f.Audit != nil {
				return fail("retired recovery changed signing state or hid another attempt")
			}
		} else {
			a := f.Audit
			if !f.Exited || a == nil || !a.Attempted || a.Started || !a.IdentityRetired || !a.SigningUnchanged || a.ExitError != "" {
				return fail("retirement did not exit and reject restart without signing")
			}
		}
	default:
		return fail("unknown terminal operation")
	}
	return nil
}
