package adapterreg_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/trust"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

func str(s string) *string { return &s }

func runtimeCaps(interrupt, ack, server bool, fronts ...adapterproto.Frontend) adapterproto.Capabilities {
	return adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{Interrupt: interrupt, AckOnBehalf: ack, ServerRequests: server, Frontends: fronts}}
}

func directCaps(n int) *adapterproto.DirectCapabilities {
	return &adapterproto.DirectCapabilities{MaxEndpoints: n}
}

func man() manifest.Manifest {
	return manifest.Manifest{ManifestVersion: 1, ID: "example", Version: "1.0.0", Protocol: adapterproto.Range{Min: 1, Max: 1},
		Entry: manifest.Entry{Program: "adapter"}, Platforms: []manifest.Platform{{OS: "linux", Arch: "amd64"}}}
}

func withBudgets(m manifest.Manifest, bs ...manifest.Budget) manifest.Manifest {
	m.Budgets = bs
	return m
}

func withRecvWaits(m manifest.Manifest, ls ...manifest.RecvWaitLimit) manifest.Manifest {
	m.RecvWaitLimits = ls
	return m
}

func withMarkers(m manifest.Manifest, replaced, truncated []manifest.Marker) manifest.Manifest {
	m.AckMarkers = &manifest.AckMarkers{Replaced: replaced, Truncated: truncated}
	return m
}

func TestAdmitDecisions(t *testing.T) {
	both := adapterproto.Capabilities{Runtime: runtimeCaps(true, true, true, "cli", "mcp").Runtime, Direct: directCaps(10)}
	tests := []struct {
		name       string
		caps       adapterproto.Capabilities
		man        manifest.Manifest
		wantReason adapterreg.AdmitReason // zero: admitted
	}{
		{"a runtime adapter", runtimeCaps(false, false, false, "cli"), man(), 0},
		{"a direct adapter", adapterproto.Capabilities{Direct: directCaps(50)}, man(), 0},
		{"an adapter with both", both, man(), 0},
		{"nothing declared", adapterproto.Capabilities{}, man(), adapterreg.ReasonNoEndpointKinds},
		{"runtime without a front end", adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{}}, man(), adapterreg.ReasonCapabilitiesInvalid},
		{"runtime with an unknown front end", runtimeCaps(false, false, false, "web"), man(), adapterreg.ReasonCapabilitiesInvalid},
		{"runtime with a repeated front end", runtimeCaps(false, false, false, "cli", "cli"), man(), adapterreg.ReasonCapabilitiesInvalid},
		{"direct limit zero", adapterproto.Capabilities{Direct: directCaps(0)}, man(), adapterreg.ReasonCapabilitiesInvalid},
		{"direct limit 1000", adapterproto.Capabilities{Direct: directCaps(1000)}, man(), 0},
		{"direct limit 1001", adapterproto.Capabilities{Direct: directCaps(1001)}, man(), adapterreg.ReasonCapabilitiesInvalid},
		{"an invalid manifest", runtimeCaps(false, false, false, "cli"), func() manifest.Manifest { m := man(); m.ID = "Bad"; return m }(), adapterreg.ReasonManifestInvalid},
		{"budget for a declared cli", runtimeCaps(false, false, false, "cli"), withBudgets(man(), manifest.Budget{Frontend: "cli", Unit: "chars", Limit: 100}), 0},
		{"budget for an undeclared mcp", runtimeCaps(false, false, false, "cli"), withBudgets(man(), manifest.Budget{Frontend: "mcp", Unit: "chars", Limit: 100}), adapterreg.ReasonBudgetFrontendUndeclared},
		{"budget for cli with no runtime", adapterproto.Capabilities{Direct: directCaps(5)}, withBudgets(man(), manifest.Budget{Frontend: "cli", Unit: "chars", Limit: 100}), adapterreg.ReasonBudgetFrontendUndeclared},
		{"budget for direct with direct endpoints", adapterproto.Capabilities{Direct: directCaps(5)}, withBudgets(man(), manifest.Budget{Frontend: "direct", Unit: "tokens", Limit: 100}), 0},
		{"budget for direct with only a runtime", runtimeCaps(false, false, false, "cli"), withBudgets(man(), manifest.Budget{Frontend: "direct", Unit: "chars", Limit: 100}), adapterreg.ReasonBudgetFrontendUndeclared},
		{"budgets for every declared front end", both, withBudgets(man(),
			manifest.Budget{Frontend: "cli", Unit: "chars", Limit: 1}, manifest.Budget{Frontend: "mcp", Unit: "tokens", Limit: 2}, manifest.Budget{Frontend: "direct", Unit: "chars", Limit: 3}), 0},
		{"recv wait limit for a declared cli", runtimeCaps(false, false, false, "cli"), withRecvWaits(man(), manifest.RecvWaitLimit{Frontend: "cli", MaxS: 8}), 0},
		{"recv wait limit for an undeclared mcp", runtimeCaps(false, false, false, "cli"), withRecvWaits(man(), manifest.RecvWaitLimit{Frontend: "mcp", MaxS: 8}), adapterreg.ReasonRecvWaitFrontendUndeclared},
		{"recv wait limit for cli with no runtime", adapterproto.Capabilities{Direct: directCaps(5)}, withRecvWaits(man(), manifest.RecvWaitLimit{Frontend: "cli", MaxS: 8}), adapterreg.ReasonRecvWaitFrontendUndeclared},
		{"recv wait limit for direct with direct endpoints: direct is not an agent front end", adapterproto.Capabilities{Direct: directCaps(5)}, withRecvWaits(man(), manifest.RecvWaitLimit{Frontend: "direct", MaxS: 8}), adapterreg.ReasonManifestInvalid},
		{"recv wait limit for direct with only a runtime: direct is not an agent front end", runtimeCaps(false, false, false, "cli"), withRecvWaits(man(), manifest.RecvWaitLimit{Frontend: "direct", MaxS: 8}), adapterreg.ReasonManifestInvalid},
		{"recv wait limit for a declared front end after one for an undeclared", runtimeCaps(false, false, false, "cli"),
			withRecvWaits(man(), manifest.RecvWaitLimit{Frontend: "cli", MaxS: 8}, manifest.RecvWaitLimit{Frontend: "mcp", MaxS: 100}), adapterreg.ReasonRecvWaitFrontendUndeclared},
		{"recv wait limit for an agent front end the runtime lists, beside direct endpoints", both, withRecvWaits(man(), manifest.RecvWaitLimit{Frontend: "cli", MaxS: 8}), 0},
		{"recv wait limits for every declared agent front end", both, withRecvWaits(man(),
			manifest.RecvWaitLimit{Frontend: "cli", MaxS: 1}, manifest.RecvWaitLimit{Frontend: "mcp", MaxS: 120}), 0},
		{"a recv wait limit above the longest an agent recv may wait", runtimeCaps(false, false, false, "cli"), withRecvWaits(man(), manifest.RecvWaitLimit{Frontend: "cli", MaxS: 121}), adapterreg.ReasonManifestInvalid},
		{"a literal marker", runtimeCaps(false, false, false, "cli"), withMarkers(man(), []manifest.Marker{{Literal: str("[saved]")}}, nil), 0},
		{"a regexp marker", runtimeCaps(false, false, false, "cli"), withMarkers(man(), nil, []manifest.Marker{{Regexp: str(`\d+ more`)}}), 0},
		{"an empty literal marker", runtimeCaps(false, false, false, "cli"), withMarkers(man(), []manifest.Marker{{Literal: str("")}}, nil), adapterreg.ReasonMarkersInvalid},
		{"an empty regexp marker", runtimeCaps(false, false, false, "cli"), withMarkers(man(), nil, []manifest.Marker{{Regexp: str("")}}), adapterreg.ReasonMarkersInvalid},
		{"a regexp that does not compile", runtimeCaps(false, false, false, "cli"), withMarkers(man(), []manifest.Marker{{Regexp: str("(")}}, nil), adapterreg.ReasonMarkersInvalid},
		{"a regexp that matches the empty text", runtimeCaps(false, false, false, "cli"), withMarkers(man(), nil, []manifest.Marker{{Regexp: str("a*")}}), adapterreg.ReasonMarkersInvalid},
		{"a marker that sets both", runtimeCaps(false, false, false, "cli"), withMarkers(man(), []manifest.Marker{{Literal: str("a"), Regexp: str("b")}}, nil), adapterreg.ReasonManifestInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapterreg.Admit(tc.caps, tc.man)
			if tc.wantReason == 0 {
				if err != nil {
					t.Fatalf("Admit = %v, want admitted", err)
				}
				return
			}
			var ae *adapterreg.AdmitError
			if !errors.As(err, &ae) || ae.Reason != tc.wantReason {
				t.Fatalf("Admit = %v, want reason %v", err, tc.wantReason)
			}
			if ae.Unwrap() == nil || ae.Error() == "" {
				t.Error("the error carries no cause")
			}
		})
	}
}

func TestAdmitReasonNames(t *testing.T) {
	for r, want := range map[adapterreg.AdmitReason]string{
		adapterreg.ReasonCapabilitiesInvalid: "capabilities_invalid", adapterreg.ReasonManifestInvalid: "manifest_invalid",
		adapterreg.ReasonNoEndpointKinds: "no_endpoint_kinds", adapterreg.ReasonBudgetFrontendUndeclared: "budget_frontend_undeclared",
		adapterreg.ReasonMarkersInvalid: "markers_invalid", adapterreg.ReasonRecvWaitFrontendUndeclared: "recv_wait_frontend_undeclared", 0: "invalid(0)", 99: "invalid(99)",
	} {
		if r.String() != want {
			t.Errorf("%d: %q, want %q", r, r.String(), want)
		}
	}
}

// TestDeclaredKindsAndSignals: which kinds an adapter owns and which control
// signals each accepts, written out for every combination of the capability flags.
func TestDeclaredKindsAndSignals(t *testing.T) {
	type sig = authz.Action
	runtimeSignals := []sig{authz.ActionSessionCreate, authz.ActionEndpointStatus}
	directSignals := []sig{authz.ActionMessageSend, authz.ActionEndpointStatus}
	sessionSignals := func(interrupt bool) []sig {
		if interrupt {
			return []sig{authz.ActionMessageSend, authz.ActionSessionClose, authz.ActionSessionInterrupt, authz.ActionEndpointStatus}
		}
		return []sig{authz.ActionMessageSend, authz.ActionSessionClose, authz.ActionEndpointStatus}
	}
	for _, interrupt := range []bool{false, true} {
		for _, hasRuntime := range []bool{false, true} {
			for _, hasDirect := range []bool{false, true} {
				if !hasRuntime && !hasDirect {
					continue
				}
				caps := adapterproto.Capabilities{}
				if hasRuntime {
					caps = runtimeCaps(interrupt, true, true, "cli")
				}
				if hasDirect {
					caps.Direct = directCaps(7)
				}
				a, err := adapterreg.Admit(caps, man())
				if err != nil {
					t.Fatal(err)
				}
				want := map[adapterreg.EndpointKind][]sig{}
				if hasRuntime {
					want[adapterreg.KindRuntime] = runtimeSignals
					want[adapterreg.KindSession] = sessionSignals(interrupt)
				}
				if hasDirect {
					want[adapterreg.KindDirect] = directSignals
				}
				for _, k := range adapterreg.Kinds() {
					if a.Declares(k) != (want[k] != nil) {
						t.Errorf("runtime=%v direct=%v interrupt=%v: Declares(%v) = %v", hasRuntime, hasDirect, interrupt, k, !a.Declares(k))
					}
					got := a.Signals(k)
					if !equalActions(got, want[k]) {
						t.Errorf("runtime=%v direct=%v interrupt=%v: Signals(%v) = %v, want %v", hasRuntime, hasDirect, interrupt, k, got, want[k])
					}
					for _, act := range authz.Actions {
						exp := false
						for _, w := range want[k] {
							exp = exp || w == act
						}
						if a.AcceptsSignal(k, act) != exp {
							t.Errorf("AcceptsSignal(%v, %v) = %v", k, act, !exp)
						}
					}
				}
				if got := len(a.DeclaredKinds()); got != len(want) {
					t.Errorf("DeclaredKinds has %d kinds, want %d", got, len(want))
				}
				wantMax := 0
				if hasDirect {
					wantMax = 7
				}
				if a.MaxDirectEndpoints() != wantMax {
					t.Errorf("MaxDirectEndpoints = %d, want %d", a.MaxDirectEndpoints(), wantMax)
				}
			}
		}
	}
}

func equalActions(a, b []authz.Action) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSignalsForInvalidKinds(t *testing.T) {
	a, _ := adapterreg.Admit(runtimeCaps(true, true, true, "cli"), man())
	for _, k := range []adapterreg.EndpointKind{0, 4, 255} {
		if a.Declares(k) || a.Signals(k) != nil || a.AcceptsSignal(k, authz.ActionEndpointStatus) {
			t.Errorf("kind %v declares or accepts something", k)
		}
		if k.Valid() {
			t.Errorf("kind %d is valid", k)
		}
	}
	if (adapterreg.Admission{}).Signals(adapterreg.KindRuntime) != nil {
		t.Error("the zero admission accepts signals")
	}
}

func TestEndpointKindTargets(t *testing.T) {
	for k, want := range map[adapterreg.EndpointKind]authz.TargetKind{
		adapterreg.KindRuntime: authz.TargetRuntime, adapterreg.KindSession: authz.TargetEndpoint, adapterreg.KindDirect: authz.TargetEndpoint,
	} {
		if k.TargetKind() != want {
			t.Errorf("%v.TargetKind() = %v, want %v", k, k.TargetKind(), want)
		}
	}
	for _, k := range []adapterreg.EndpointKind{0, 4, 255} {
		if got := k.TargetKind(); got != 0 {
			t.Errorf("%v.TargetKind() = %v, want the zero target kind", k, got)
		}
	}
	for k, want := range map[adapterreg.EndpointKind]string{adapterreg.KindRuntime: "runtime", adapterreg.KindSession: "session", adapterreg.KindDirect: "direct", 9: "invalid(9)"} {
		if k.String() != want {
			t.Errorf("String(%d) = %q", k, k.String())
		}
	}
	// The signals an endpoint accepts are actions authorization can pair with that
	// kind of object: otherwise the declaration would name rules that can never
	// be written.
	a, _ := adapterreg.Admit(adapterproto.Capabilities{Runtime: runtimeCaps(true, true, true, "cli").Runtime, Direct: directCaps(5)}, man())
	ep := endpointID(t)
	for _, k := range adapterreg.Kinds() {
		object := authz.LocalEndpoint(ep)
		if k == adapterreg.KindRuntime {
			object = authz.RuntimeEndpoint(ep)
		}
		for _, act := range a.Signals(k) {
			if _, err := authz.NewPolicy([]authz.Rule{authz.Allow(authz.Members(), object, act, authz.Conditions{})}); err != nil {
				t.Errorf("%v accepts %v, which authorization cannot pair with its object: %v", k, act, err)
			}
		}
	}
}

func endpointID(t *testing.T) ident.EndpointID {
	t.Helper()
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{7}, ident.EndpointIDLen))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBudgetsAndMarkers(t *testing.T) {
	caps := adapterproto.Capabilities{Runtime: runtimeCaps(true, true, true, "cli", "mcp").Runtime, Direct: directCaps(5)}
	m := withBudgets(man(),
		manifest.Budget{Frontend: "cli", Unit: "chars", Limit: 27000},
		manifest.Budget{Frontend: "mcp", Unit: "tokens", Limit: 22500})
	a, err := adapterreg.Admit(caps, m)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := a.Budget("cli"), (handoff.Budget{Unit: handoff.UnitChars, Limit: 27000}); got != want {
		t.Errorf("cli budget = %+v, want %+v", got, want)
	}
	if got, want := a.Budget("mcp"), (handoff.Budget{Unit: handoff.UnitTokens, Limit: 22500}); got != want {
		t.Errorf("mcp budget = %+v, want %+v", got, want)
	}
	// A front end without a budget gets the conservative default: 9,000 tokens.
	if got, want := a.Budget("direct"), (handoff.Budget{Unit: handoff.UnitTokens, Limit: 9000}); got != want {
		t.Errorf("direct budget = %+v, want the default %+v", got, want)
	}
	if got := (adapterreg.Admission{}).Budget("cli"); got != handoff.DefaultBudget() {
		t.Errorf("the zero admission's budget = %+v", got)
	}
	if got := a.Budget("nonsense"); got != handoff.DefaultBudget() {
		t.Errorf("an unknown front end's budget = %+v", got)
	}
}

// TestPartBudgets: a long message is cut for every front end its endpoint can be read
// through, the default counting for one that declares nothing, and a direct endpoint
// for the front end direct alone.
func TestPartBudgets(t *testing.T) {
	caps := adapterproto.Capabilities{Runtime: runtimeCaps(true, true, true, "cli", "mcp").Runtime, Direct: directCaps(5)}
	a, err := adapterreg.Admit(caps, withBudgets(man(), manifest.Budget{Frontend: "cli", Unit: "chars", Limit: 27000}))
	if err != nil {
		t.Fatal(err)
	}
	cli := handoff.Budget{Unit: handoff.UnitChars, Limit: 27000}
	for _, tc := range []struct {
		name string
		kind adapterreg.EndpointKind
		want []handoff.Budget
	}{
		{"a session: cli as declared, mcp as the default", adapterreg.KindSession, []handoff.Budget{cli, handoff.DefaultBudget()}},
		{"a runtime endpoint as a session", adapterreg.KindRuntime, []handoff.Budget{cli, handoff.DefaultBudget()}},
		{"a direct endpoint: the front end direct, the default here", adapterreg.KindDirect, []handoff.Budget{handoff.DefaultBudget()}},
	} {
		if got := a.PartBudgets(tc.kind); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
	if got := (adapterreg.Admission{}).PartBudgets(adapterreg.KindSession); len(got) != 0 {
		t.Errorf("the zero admission names no front end, got %+v", got)
	}
}

// TestRecvWait: the wait limit of a front end is the one the manifest declares,
// and the default for a front end it does not name.
func TestRecvWait(t *testing.T) {
	if adapterreg.DefaultRecvWait != 8*time.Second {
		t.Fatalf("DefaultRecvWait = %v, want 8s: below the shortest client timeout of 10s", adapterreg.DefaultRecvWait)
	}
	if adapterreg.DefaultRecvWait >= 10*time.Second {
		t.Fatalf("DefaultRecvWait = %v must stay below the 10s at which a shell client returns or kills", adapterreg.DefaultRecvWait)
	}
	caps := adapterproto.Capabilities{Runtime: runtimeCaps(true, true, true, "cli", "mcp").Runtime, Direct: directCaps(5)}
	m := withRecvWaits(man(),
		manifest.RecvWaitLimit{Frontend: "cli", MaxS: 3},
		manifest.RecvWaitLimit{Frontend: "mcp", MaxS: manifest.MaxRecvWaitSeconds})
	a, err := adapterreg.Admit(caps, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		f    adapterproto.Frontend
		want time.Duration
	}{
		{"a declared short limit", "cli", 3 * time.Second},
		{"a declared limit at the longest an agent recv may wait", "mcp", 120 * time.Second},
		{"direct is not an agent front end and gets the default", "direct", adapterreg.DefaultRecvWait},
		{"an unknown front end gets the default", "nonsense", adapterreg.DefaultRecvWait},
		{"an empty front end gets the default", "", adapterreg.DefaultRecvWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.RecvWait(tc.f); got != tc.want {
				t.Errorf("RecvWait(%q) = %v, want %v", tc.f, got, tc.want)
			}
		})
	}
	t.Run("an agent front end the runtime declares but the manifest gives no value gets the default", func(t *testing.T) {
		cliOnly, err := adapterreg.Admit(caps, withRecvWaits(man(), manifest.RecvWaitLimit{Frontend: "cli", MaxS: 3}))
		if err != nil {
			t.Fatal(err)
		}
		if got := cliOnly.RecvWait("mcp"); got != adapterreg.DefaultRecvWait {
			t.Errorf("RecvWait(mcp) = %v, want the default", got)
		}
		if got := cliOnly.RecvWait("cli"); got != 3*time.Second {
			t.Errorf("RecvWait(cli) = %v, want the declared 3s", got)
		}
	})
	t.Run("a manifest without limits gives the default everywhere", func(t *testing.T) {
		none, err := adapterreg.Admit(caps, man())
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP} {
			if got := none.RecvWait(f); got != adapterreg.DefaultRecvWait {
				t.Errorf("RecvWait(%q) = %v, want the default", f, got)
			}
		}
	})
	t.Run("the zero admission gives the default", func(t *testing.T) {
		if got := (adapterreg.Admission{}).RecvWait("cli"); got != adapterreg.DefaultRecvWait {
			t.Errorf("RecvWait = %v, want the default", got)
		}
	})
	t.Run("a declared limit may be longer than the default", func(t *testing.T) {
		if got := a.RecvWait("mcp"); got <= adapterreg.DefaultRecvWait {
			t.Errorf("RecvWait(mcp) = %v: a front end with a long timeout must be able to wait longer than the default", got)
		}
	})
}

// TestMarkersDriveTheAckCheck: markers declared in a manifest reach
// handoff.CheckBeforeAck compiled, so a replaced or cut result is refused with
// its cause and a whole one is allowed.
func TestMarkersDriveTheAckCheck(t *testing.T) {
	m := withMarkers(man(),
		[]manifest.Marker{{Literal: str("[result saved to file]")}, {Regexp: str(`^Saved to \S+$`)}},
		[]manifest.Marker{{Literal: str("...[truncated]")}})
	a, err := adapterreg.Admit(runtimeCaps(false, true, false, "cli"), m)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"lease":"x","messages":[],"more":0}`
	for _, tc := range []struct {
		name     string
		observed string
		verdict  handoff.AckVerdict
		reason   handoff.AckReason
	}{
		{"the whole output", expected + "\n", handoff.AckAllowed, handoff.AckSeenInFull},
		{"a literal notice beside the output", expected + "\n[result saved to file]", handoff.AckRefused, handoff.AckReplaced},
		{"a notice that stands in for it, matched by regexp", "Saved to /tmp/out.txt", handoff.AckRefused, handoff.AckReplaced},
		{"a cut", expected + "...[truncated]", handoff.AckRefused, handoff.AckTruncated},
		{"something else", "nothing to see", handoff.AckRefused, handoff.AckContentMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, r := handoff.CheckBeforeAck(a.Markers(), tc.observed, expected)
			if v != tc.verdict || r != tc.reason {
				t.Fatalf("CheckBeforeAck = %v, %v, want %v, %v", v, r, tc.verdict, tc.reason)
			}
		})
	}
	// With no markers declared only containment decides.
	b, _ := adapterreg.Admit(runtimeCaps(false, true, false, "cli"), man())
	if v, r := handoff.CheckBeforeAck(b.Markers(), expected+"\n[result saved to file]", expected); v != handoff.AckAllowed || r != handoff.AckSeenInFull {
		t.Errorf("without markers: %v, %v", v, r)
	}
}

func TestAdmissionIsIndependentOfItsInputs(t *testing.T) {
	caps := runtimeCaps(true, true, true, "cli", "mcp")
	a, err := adapterreg.Admit(caps, man())
	if err != nil {
		t.Fatal(err)
	}
	caps.Runtime.Interrupt = false
	caps.Runtime.Frontends[0] = "mcp"
	if !a.Capabilities().Runtime.Interrupt || a.Capabilities().Runtime.Frontends[0] != "cli" {
		t.Error("the admission shares memory with the capabilities it was given")
	}
	got := a.Capabilities()
	got.Runtime.Frontends[1] = "cli"
	if a.Capabilities().Runtime.Frontends[1] != "mcp" {
		t.Error("Capabilities returns the admission's own slice")
	}
}

// A value that is not an endpoint kind must not be judged as an ordinary
// endpoint: its target kind is the zero one, and authorization refuses a request
// about it as malformed, while the three real kinds are well formed.
func TestInvalidEndpointKindFailsClosedInAuthorization(t *testing.T) {
	nodeBytes := bytes.Repeat([]byte{1}, ident.NodeIDLen)
	endpointBytes := bytes.Repeat([]byte{2}, ident.EndpointIDLen)
	node, err := ident.NodeIDFromBytes(nodeBytes)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ident.EndpointIDFromBytes(endpointBytes)
	if err != nil {
		t.Fatal(err)
	}
	reason := func(k adapterreg.EndpointKind) authz.Reason {
		return authz.Evaluate(authz.Policy{}, authz.Request{
			Now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC), Trust: trust.AllowedDirect, Stage: authz.StageIntake,
			Sender: authz.Sender{Node: node}, Action: authz.ActionEndpointStatus,
			Target: authz.Target{Kind: k.TargetKind(), Endpoint: endpoint},
		}).Reason
	}
	for _, k := range []adapterreg.EndpointKind{0, 4, 255} {
		if got := reason(k); got != authz.ReasonMalformedRequest {
			t.Errorf("kind %d: reason %v, want malformed request", k, got)
		}
	}
	for _, k := range adapterreg.Kinds() {
		if got := reason(k); got == authz.ReasonMalformedRequest {
			t.Errorf("kind %v: a real kind is refused as malformed", k)
		}
	}
}
