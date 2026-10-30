package inproc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestInstanceDisableJudgesTheActualCreationOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, alteration, violation string
		crash                       bool
	}{
		{name: "normal-unknown"},
		{name: "unknown-without-feedback", alteration: "missing-feedback", violation: "corresponding terminal feedback"},
		{name: "recovery-created", crash: true},
		{name: "created-without-reply", crash: true, alteration: "missing-reply", violation: "corresponding terminal feedback"},
		{name: "created-without-native", crash: true, alteration: "missing-native", violation: "no real native creation"},
		{name: "created-with-another-attempt", crash: true, alteration: "another-attempt", violation: "no real native creation"},
		{name: "created-with-another-endpoint", crash: true, alteration: "another-endpoint", violation: "no real native creation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBindings()
			// Bindings still inspect and operate on the real fixture. Only the
			// evidence presented to the shared recipe judgement is altered.
			actual := b.Management
			b.Management.Versions = func(e scenario.Env) (recipes.ManagementVersions, error) {
				return actual.Versions(instanceFixture(e))
			}
			b.Management.DisableAdapter = func(e scenario.Env, q recipes.ManagementRequest, instance string) (recipes.ManagementOutcome, error) {
				return actual.DisableAdapter(instanceFixture(e), q, instance)
			}
			b.Management.InspectInstance = func(e scenario.Env, instance string) (recipes.InstanceFacts, error) {
				return actual.InspectInstance(instanceFixture(e), instance)
			}
			closed := b.Recovery.CheckClosedRecords
			b.Recovery.CheckClosedRecords = func(e scenario.Env) error { return closed(instanceFixture(e)) }
			var recipe recipes.ScriptRecipe
			for _, r := range recipes.New(b).Scripts {
				if r.Script.Name == "management-instance" {
					recipe = r
					break
				}
			}
			if recipe.Script.Name == "" {
				t.Fatal("management-instance recipe is missing")
			}
			altered := 0
			wrap := func(h *harness.H) *instanceEvidence {
				return &instanceEvidence{H: h, alteration: tc.alteration, altered: &altered}
			}
			s := &harness.WalkScenario{Script: recipe.Script, Options: recipeOptions(recipe.Needs),
				Drain: func(h *harness.H) error { return recipe.Drain(wrap(h)) },
				Check: func(h *harness.H) error {
					message, err := instanceControl(h)
					if err != nil {
						return err
					}
					status, err := h.Agent("sender").Status(h.Ctx(), fakeagent.StatusInput{MessageID: message})
					if err != nil {
						return err
					}
					if len(status.Recipients) != 1 {
						return fmt.Errorf("creation has %d recipients", len(status.Recipients))
					}
					if tc.crash {
						if status.Recipients[0].State != adapterproto.DeliveryDelivered || status.ControlReply == nil || status.ControlReply.Create == nil || status.ControlReply.Create.Outcome != adapterproto.CreateReplyCreated {
							return fmt.Errorf("recovery did not actually create: %+v", status)
						}
					} else if status.Recipients[0].State != adapterproto.DeliveryUnknown || status.ControlReply != nil {
						return fmt.Errorf("normal disable did not remain unknown: %+v", status)
					}
					return recipe.Check(wrap(h))
				},
			}
			plan := crashwalk.RunPlan{Record: true}
			if tc.crash {
				point, err := fsx.ParsePoint("adapter-disable/instances.rename#1")
				if err != nil {
					t.Fatal(err)
				}
				plan = crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, Point: point}}}
			}
			r, err := harness.NewWalkBackend(t).Run(context.Background(), s, plan)
			if err != nil {
				t.Fatal(err)
			}
			if tc.crash && (len(r.Reached) != 1 || !r.Reached[0] || len(r.Hits) != 2) {
				t.Fatalf("real disable cut: reached=%v generations=%d", r.Reached, len(r.Hits))
			}
			if tc.violation == "" {
				if len(r.Violations) != 0 {
					t.Fatalf("actual result rejected: %v", r.Violations)
				}
			} else if altered == 0 || len(r.Violations) != 1 || !strings.Contains(r.Violations[0], tc.violation) {
				t.Fatalf("altered evidence accepted: alterations=%d violations=%v", altered, r.Violations)
			}
		})
	}
}

type instanceEvidence struct {
	*harness.H
	alteration string
	altered    *int
}

func instanceFixture(e scenario.Env) scenario.Env {
	if evidence, ok := e.(*instanceEvidence); ok {
		return evidence.H
	}
	return e
}

func instanceControl(h *harness.H) (adapterproto.MessageID, error) {
	log, err := h.Ledger()
	if err != nil {
		return "", err
	}
	for _, entry := range log.Kind(ledger.KindClientResult) {
		if entry.Attrs.Action == "control" && entry.Attrs.Category == "ok" {
			return adapterproto.MessageID(entry.Attrs.MessageID), nil
		}
	}
	return "", fmt.Errorf("no successful creation request in the external ledger")
}

func (e *instanceEvidence) Agent(name string) fakeagent.Agent {
	return instanceAgent{Agent: e.H.Agent(name), evidence: e}
}

func (e *instanceEvidence) Ledger() (*ledger.Log, error) {
	log, err := e.H.Ledger()
	if err != nil || (e.alteration != "missing-native" && e.alteration != "another-attempt") {
		return log, err
	}
	copy := ledger.NewMemory()
	for _, entry := range log.Entries() {
		if entry.Kind == ledger.KindRuntimeSessionCreated {
			(*e.altered)++
			if e.alteration == "missing-native" {
				continue
			}
			entry.Attrs.Attempt = "another-attempt"
		}
		writer, err := copy.Open(entry.Writer, int(entry.PID), e.Clock().Now)
		if err != nil {
			return nil, err
		}
		if err := writer.Append(entry.Kind, entry.Attrs); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
	}
	return copy.Log()
}

type instanceAgent struct {
	fakeagent.Agent
	evidence *instanceEvidence
}

func (a instanceAgent) Recv(ctx context.Context, in fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	r, err := a.Agent.Recv(ctx, in)
	if err != nil || a.evidence.alteration != "missing-feedback" {
		return r, err
	}
	message, err := instanceControl(a.evidence.H)
	if err != nil {
		return r, err
	}
	kept := r.Page.Messages[:0]
	for _, m := range r.Page.Messages {
		if strings.Contains(m.Body, string(message)) {
			(*a.evidence.altered)++
			continue
		}
		kept = append(kept, m)
	}
	r.Page.Messages = kept
	return r, nil
}

func (a instanceAgent) Status(ctx context.Context, in fakeagent.StatusInput) (fakeagent.StatusResult, error) {
	r, err := a.Agent.Status(ctx, in)
	if err != nil || r.ControlReply == nil {
		return r, err
	}
	switch a.evidence.alteration {
	case "missing-reply":
		r.ControlReply = nil
		(*a.evidence.altered)++
	case "another-endpoint":
		address, err := ident.ParseAddress(a.evidence.Addr("sender"))
		if err != nil {
			return r, err
		}
		reply := *r.ControlReply
		create := *reply.Create
		create.SessionEndpoint = adapterproto.EndpointRef(address.Endpoint.String())
		reply.Create = &create
		r.ControlReply = &reply
		(*a.evidence.altered)++
	}
	return r, nil
}
