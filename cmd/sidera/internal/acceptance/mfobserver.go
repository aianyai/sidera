//go:build diagnostic

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// These private projections contain only identifiers, fixed statuses, counts and
// times. Store and ledger records must never be handed to the JSON encoder.
type mfObservation struct {
	Boundary       string       `json:"boundary"`
	Snapshot       string       `json:"snapshot"`
	Status         string       `json:"status"`
	Clock          string       `json:"clock"`
	Now            *time.Time   `json:"now,omitempty"`
	MergeWindow    string       `json:"merge_window"`
	Sender         string       `json:"sender,omitempty"`
	Runtime        string       `json:"runtime,omitempty"`
	RuntimeMatches int          `json:"runtime_matches"`
	RuntimeState   uint8        `json:"runtime_state"`
	ControlMatches int          `json:"control_matches"`
	Control        string       `json:"control,omitempty"`
	SourceMatches  int          `json:"source_matches"`
	SourceControl  bool         `json:"source_control"`
	SourceLost     bool         `json:"source_lost"`
	SourceStates   []mfState    `json:"source_states,omitempty"`
	Feedback       []mfFeedback `json:"feedback,omitempty"`
	Notices        []mfNotice   `json:"notices,omitempty"`
	DamagedRecords int          `json:"damaged_records"`
	UnknownRecords int          `json:"unknown_records"`
}

type mfState struct {
	Recipient string            `json:"recipient"`
	State     delivery.State    `json:"state"`
	Sub       delivery.SubState `json:"sub"`
	StateAt   time.Time         `json:"state_at"`
}

type mfFeedback struct {
	Kind      delivery.Kind `json:"kind"`
	At        time.Time     `json:"at"`
	Recipient string        `json:"recipient"`
}

type mfNotice struct {
	Message        string            `json:"message"`
	Sender         string            `json:"sender"`
	Recipient      string            `json:"recipient"`
	State          delivery.State    `json:"state"`
	Sub            delivery.SubState `json:"sub"`
	StateAt        time.Time         `json:"state_at"`
	CoverageKind   delivery.Kind     `json:"coverage_kind"`
	CoveredTarget  string            `json:"covered_target"`
	PendingMatches int               `json:"pending_matches"`
}

func mfObserveCheck(plan crashwalk.RunPlan, name string, e *environment) {
	// A diagnostic build alone must not add reads, clock access or output.
	if os.Getenv("SIDERA_CI_DIAGNOSTIC_MF_OBSERVER") != "1" || name != "management-instance" || len(plan.Targets) != 1 || plan.Targets[0].Gen != 1 || plan.Targets[0].Point.String() != "source-update/out.write#4" || plan.Trigger != nil {
		return
	}
	o := mfObservation{Boundary: "original_check_failed_before_stop", Snapshot: "not_frozen", Status: "unavailable", Clock: "unavailable", MergeWindow: "unavailable"}
	defer func() { mfPrint(o) }()
	if e.check() != nil {
		return
	}
	now := e.fixture.Now()
	o.Clock, o.Now = "available", &now
	sender, ok := e.endpoints["sender"]
	if !ok {
		return
	}
	senderID, err := ident.ParseEndpointID(string(sender.ref))
	if err != nil {
		return
	}
	runtime, err := ident.ParseAddress(e.fixture.runtimeAddresses["management-target"])
	if err != nil {
		return
	}
	log, err := e.fixture.Ledger()
	if err != nil {
		return
	}
	// The caller's original quiet boundary is the only boundary: this scan is
	// read-only but is not an atomic or frozen snapshot of the serving daemon.
	scan, _, err := readSessionRecords(e)
	if err != nil {
		return
	}
	o = mfProject(scan, log.Entries(), senderID, runtime, now)
}

func mfPrint(o mfObservation) {
	raw, err := json.Marshal(o)
	if err != nil {
		fmt.Fprintln(os.Stdout, `[DEBUG-mf] {"status":"encoding_unavailable"}`)
		return
	}
	fmt.Fprintln(os.Stdout, "[DEBUG-mf] "+string(raw))
}

func mfProject(scan *store.Scan, entries []ledger.Entry, sender ident.EndpointID, runtime ident.Address, now time.Time) mfObservation {
	o := mfObservation{Boundary: "original_check_failed_before_stop", Snapshot: "not_frozen", Status: "available", Clock: "available", Now: &now, MergeWindow: "unavailable", Sender: sender.String(), Runtime: runtime.String(), DamagedRecords: len(scan.Damaged), UnknownRecords: len(scan.Unknown)}
	for _, ep := range scan.Endpoints {
		if ep.ID == runtime.Endpoint {
			o.RuntimeMatches++
			o.RuntimeState = uint8(ep.Status)
		}
	}
	if o.RuntimeMatches != 1 {
		o.RuntimeState = 0
	}
	var control delivery.MessageID
	for _, entry := range entries {
		if entry.Writer == "agent/"+sender.String() && entry.Step == "queue-instance-records" && entry.Kind == ledger.KindClientResult && entry.Attrs.Action == "control" && entry.Attrs.Category == "ok" {
			o.ControlMatches++
			control = delivery.MessageID(entry.Attrs.MessageID)
		}
	}
	if o.ControlMatches != 1 {
		return o
	}
	o.Control = string(control)
	var source store.OutRecord
	for _, out := range scan.Out {
		if out.Key.Sender == sender && out.Key.Message == control && out.Key.Control {
			o.SourceMatches++
			source = out
		}
	}
	if o.SourceMatches != 1 {
		return o
	}
	o.SourceControl, o.SourceLost = source.Key.Control, source.Lost != nil
	if source.Lost == nil {
		for _, tracked := range source.Outbound.Deliveries() {
			r := tracked.Delivery
			o.SourceStates = append(o.SourceStates, mfState{tracked.Address.String(), r.State, r.Sub, r.StateAt})
		}
	} else {
		for i, r := range source.Lost.Deliveries {
			o.SourceStates = append(o.SourceStates, mfState{source.Key.Recipients[i].String(), r.State, r.Sub, r.StateAt})
		}
	}
	for _, item := range source.Feedback {
		o.Feedback = append(o.Feedback, mfFeedback{item.Feedback.Kind, item.At, item.Ref.Recipient.String()})
	}
	for _, in := range scan.In {
		for _, cover := range in.Covers {
			// Ownership survives removal of an already covered feedback item.
			if cover.Out != source.Name() {
				continue
			}
			n := mfNotice{Message: string(in.Key.Message), Sender: ident.Address{Node: in.Key.SenderNode, Endpoint: in.Key.Sender}.String(), Recipient: in.Key.Recipient.String(), State: in.Record.State, Sub: in.Record.Sub, StateAt: in.Record.StateAt, CoverageKind: cover.Kind, CoveredTarget: cover.Recipient.String()}
			for _, item := range source.Feedback {
				if cover.Covers(source.Name(), item) {
					n.PendingMatches++
				}
			}
			o.Notices = append(o.Notices, n)
		}
	}
	return o
}
