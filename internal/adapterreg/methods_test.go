package adapterreg_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// matrix is the requirement of each method for a runtime, a session and a direct
// endpoint, written out from the specification: F forbidden, O optional, R
// required. Every method of the protocol has a row.
var matrix = map[string]string{
	"initialize": "RRR", "ping": "RRR", "$/cancel_request": "RRR", "shutdown": "RRR",
	"adapter.start": "RRR", "adapter.probe": "RRR", "adapter.availability": "RRR",
	"session.create": "RRF", "prompt.deliver": "RRF", "prompt.reconcile": "RRF", "turn.started": "RRF", "turn.ended": "RRF",
	"session.close": "RRF", "prompt.persisted": "RRF",
	"checkpoint.put":    "OOO",
	"session.interrupt": "OOF", "recv.observed": "FOF", "native_request.handled": "RRF",
	"endpoint.register": "FFR", "endpoint.recv": "FFR", "endpoint.ack": "FFR", "endpoint.send": "FFR", "endpoint.pending": "FFR",
	"endpoint.unregister": "FFO", "endpoint.message_status": "FFO",
}

func requirementOf(c byte) adapterreg.Requirement {
	switch c {
	case 'F':
		return adapterreg.Forbidden
	case 'O':
		return adapterreg.Optional
	}
	return adapterreg.Required
}

func TestMethodMatrix(t *testing.T) {
	kinds := []adapterreg.EndpointKind{adapterreg.KindRuntime, adapterreg.KindSession, adapterreg.KindDirect}
	methods := adapterproto.Methods()
	if len(methods) != len(matrix) {
		t.Fatalf("the protocol has %d methods, the test matrix %d", len(methods), len(matrix))
	}
	for _, m := range methods {
		row, ok := matrix[m.Name]
		if !ok {
			t.Errorf("method %s has no row in the matrix", m.Name)
			continue
		}
		for i, k := range kinds {
			want := requirementOf(row[i])
			if got := adapterreg.RequirementOf(k, m.Name); got != want {
				t.Errorf("RequirementOf(%v, %s) = %v, want %v", k, m.Name, got, want)
			}
			if got := adapterreg.MethodAllowed(k, m.Name); got != (want != adapterreg.Forbidden) {
				t.Errorf("MethodAllowed(%v, %s) = %v", k, m.Name, got)
			}
		}
	}
}

func TestMethodAllowedUnknowns(t *testing.T) {
	for _, k := range []adapterreg.EndpointKind{adapterreg.KindRuntime, adapterreg.KindSession, adapterreg.KindDirect} {
		for _, m := range []string{"", "nope", "session.frobnicate", "Ping"} {
			if adapterreg.MethodAllowed(k, m) || adapterreg.RequirementOf(k, m) != adapterreg.Forbidden {
				t.Errorf("unknown method %q is allowed for %v", m, k)
			}
		}
	}
	for _, k := range []adapterreg.EndpointKind{0, 4} {
		if adapterreg.MethodAllowed(k, "ping") {
			t.Errorf("an invalid kind %d may call ping", k)
		}
	}
	for r, want := range map[adapterreg.Requirement]string{adapterreg.Forbidden: "forbidden", adapterreg.Optional: "optional", adapterreg.Required: "required", 9: "invalid"} {
		if r.String() != want {
			t.Errorf("%d: %q", r, r.String())
		}
	}
}

// TestCheck compares Check with an independent statement of the rule for every
// combination of declared capabilities, kind and method, and counts how often
// each answer came up so no answer is left untested.
func TestCheck(t *testing.T) {
	kinds := []adapterreg.EndpointKind{adapterreg.KindRuntime, adapterreg.KindSession, adapterreg.KindDirect}
	gates := map[string]string{"session.interrupt": "interrupt", "recv.observed": "ack", "native_request.handled": "server"}
	answers := map[int]int{}
	for mask := 0; mask < 16; mask++ {
		interrupt, ack, server, direct := mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0
		for _, withRuntime := range []bool{false, true} {
			caps := adapterproto.Capabilities{}
			if withRuntime {
				caps = runtimeCaps(interrupt, ack, server, "cli")
			}
			if direct {
				caps.Direct = directCaps(3)
			}
			if !withRuntime && !direct {
				continue
			}
			a, err := adapterreg.Admit(caps, man())
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range adapterproto.Methods() {
				row := matrix[m.Name]
				for i, k := range kinds {
					want := 0
					declared := (k == adapterreg.KindDirect && direct) || (k != adapterreg.KindDirect && withRuntime)
					switch {
					case row[i] == 'F':
						want = adapterproto.CodeWrongEndpointKind
					case !declared:
						want = adapterproto.CodeCapabilityNotDeclared
					default:
						switch gates[m.Name] {
						case "interrupt":
							if !(withRuntime && interrupt) {
								want = adapterproto.CodeCapabilityNotDeclared
							}
						case "ack":
							if !(withRuntime && ack) {
								want = adapterproto.CodeCapabilityNotDeclared
							}
						case "server":
							if !(withRuntime && server) {
								want = adapterproto.CodeCapabilityNotDeclared
							}
						}
					}
					if got := a.Check(k, m.Name); got != want {
						t.Errorf("runtime=%v interrupt=%v ack=%v server=%v direct=%v: Check(%v, %s) = %d, want %d", withRuntime, interrupt, ack, server, direct, k, m.Name, got, want)
					}
					answers[want]++
				}
			}
		}
	}
	for _, code := range []int{0, adapterproto.CodeWrongEndpointKind, adapterproto.CodeCapabilityNotDeclared} {
		if answers[code] < 50 {
			t.Errorf("answer %d came up only %d times", code, answers[code])
		}
	}
	a, _ := adapterreg.Admit(runtimeCaps(true, true, true, "cli"), man())
	if got := a.Check(adapterreg.KindSession, "no.such.method"); got != adapterproto.CodeMethodNotFound {
		t.Errorf("Check of an unknown method = %d", got)
	}
	if got := (adapterreg.Admission{}).Check(adapterreg.KindSession, "prompt.deliver"); got != adapterproto.CodeCapabilityNotDeclared {
		t.Errorf("the zero admission allows prompt.deliver: %d", got)
	}
}
