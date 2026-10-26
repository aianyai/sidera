package manageapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
)

// The table of the owner's methods: each is authorized as the operation of the
// catalogue it is mapped to, and which of them carry a version and which change
// state are what the catalogue says. The expectations are written out by hand from
// the mapping of the commands to the operations.
func TestEachMethodOfTheOwnerIsTheOperationOfTheCatalogueItIsMappedTo(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		method  string
		kind    admin.OpKind
		changes bool // SideEffecting
		version bool // IsConfigChange
	}{
		{"status", admin.OpRead, false, false},
		{"alerts.list", admin.OpRead, false, false},
		{"alerts.ack", admin.OpAcknowledgeAlert, true, false},
		{"identity.export_revocation", admin.OpExportRevocation, false, false},
		{"identity.confirm_backup", admin.OpConfirmRevocationBackup, true, false},
		{"identity.retire", admin.OpRetireIdentity, true, false},
		{"clock.clear_rollback", admin.OpClearClockRollback, true, false},
		{"node.show", admin.OpRead, false, false},
		{"node.set", admin.OpChangeNodeSettings, true, true},
		{"endpoints.set", admin.OpChangeEndpointSettings, true, true},
		{"adapters.install", admin.OpInstallAdapter, true, false},
		{"adapters.list", admin.OpRead, false, false},
		{"adapters.enable", admin.OpEnableAdapter, true, true},
		{"adapters.disable", admin.OpDisableAdapter, true, true},
		{"presets.list", admin.OpRead, false, false},
		{"presets.add", admin.OpChangePreset, true, true},
		{"presets.remove", admin.OpChangePreset, true, true},
	} {
		m := r.srv.methods[tc.method]
		if m == nil {
			t.Errorf("no method %s", tc.method)
			continue
		}
		if m.op.Kind != tc.kind || m.op.SideEffecting() != tc.changes || m.op.IsConfigChange() != tc.version {
			t.Errorf("%s is %v (changes %v, versioned %v), want %v (%v, %v)", tc.method,
				m.op.Kind, m.op.SideEffecting(), m.op.IsConfigChange(), tc.kind, tc.changes, tc.version)
		}
		// The owner is allowed every one of them, and by the catalogue only: the
		// export is never allowed to a person who may only view.
		if v := admin.Authorize(r.srv.list, admin.LocalOwner(), m.op, t0); !v.Allowed {
			t.Errorf("%s is not allowed to the local owner: %v", tc.method, v.Reason)
		}
	}
}

// The params of each method are read strictly: a member the method does not take, a
// value of the wrong type and an identifier, digest, node or address that is not
// well formed are refused as invalid arguments naming the place, and nothing
// reaches the engine.
func TestTheParamsOfTheOwnersMethodsAreReadStrictly(t *testing.T) {
	r := newRig(t)
	id := opID(1)
	for _, tc := range []struct {
		name, method, body string
		path               string
	}{
		{"an alert that is not an identifier", "alerts.ack", `{"operation_id":"` + id + `","params":{"alert":"disk_low"}}`, "params.alert"},
		{"an alert of another length", "alerts.ack", `{"operation_id":"` + id + `","params":{"alert":"ab"}}`, "params.alert"},
		{"a digest of capitals", "identity.confirm_backup", `{"operation_id":"` + id + `","params":{"digest":"` + strings.Repeat("AB", 32) + `"}}`, "params.digest"},
		{"a digest that is short", "identity.confirm_backup", `{"operation_id":"` + id + `","params":{"digest":"abcd"}}`, "params.digest"},
		{"a digest of zeros", "identity.confirm_backup", `{"operation_id":"` + id + `","params":{"digest":"` + strings.Repeat("00", 32) + `"}}`, "params.digest"},
		{"a node that is not an identifier", "identity.retire", `{"operation_id":"` + id + `","params":{"node":"here"}}`, "params.node"},
		{"an endpoint that is not an address", "endpoints.set", `{"operation_id":"` + id + `","expected_version":1,"params":{"endpoint":"x","settings":{"backlog_limit":3}}}`, "params.endpoint"},
		{"settings that are absent", "node.set", `{"operation_id":"` + id + `","expected_version":1,"params":{}}`, "params.settings"},
		{"settings that are absent for an endpoint", "endpoints.set", `{"operation_id":"` + id + `","expected_version":1,"params":{"endpoint":"` + addressOf(1) + `"}}`, "params.settings"},
		{"a member of the clear that is not there", "clock.clear_rollback", `{"operation_id":"` + id + `","params":{"force":true}}`, "params.force"},
		{"a member of the retirement that is not there", "identity.retire", `{"operation_id":"` + id + `","params":{"node":"` + ownNode.String() + `","yes":true}}`, "params.yes"},
		{"an unknown member of the list", "alerts.list", `{"params":{"all":true}}`, "params.all"},
		{"a null for a member", "alerts.ack", `{"operation_id":"` + id + `","params":{"alert":null}}`, "params.alert"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out := r.post(tc.method, tc.body)
			e := r.wantError(400, "invalid_argument", "malformed_request", status, out)
			if tc.path != "" && e.Error.Details["path"] != tc.path {
				t.Errorf("the fault is placed at %v, want %q", e.Error.Details["path"], tc.path)
			}
			if n := len(r.svc.calls()); n != 0 {
				t.Errorf("a refused request reached the engine %d times", n)
			}
		})
	}
	// A request for another node's endpoint never reaches the engine either.
	other, err := ident.NodeIDFromBytes([]byte(strings.Repeat("\x2a", ident.NodeIDLen)))
	if err != nil {
		t.Fatal(err)
	}
	foreign := ident.Address{Node: other, Endpoint: endpointOf(9)}.String()
	status, out := r.post("endpoints.set", `{"operation_id":"`+id+`","expected_version":1,"params":{"endpoint":"`+foreign+`","settings":{"backlog_limit":3}}}`)
	r.wantError(404, "not_found", "endpoint_unknown", status, out)
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("the engine was asked %d times", n)
	}
}

// What the engine is asked for each change: the name of the method, the version the
// request names for the two settings, and the operation of the catalogue. A patch of
// settings has one fingerprint however its members are ordered and spaced, and
// another when a value differs.
func TestTheCommandsOfTheOwnersChangesAndTheFingerprintOfAPatch(t *testing.T) {
	r := newRig(t)
	send := func(method, body string) engine.ManagementCommand {
		t.Helper()
		before := len(r.svc.calls())
		if status, out := r.post(method, body); status != 200 {
			t.Fatalf("%s answered %d %s", body, status, out)
		}
		calls := r.svc.calls()
		if len(calls) != before+1 {
			t.Fatalf("%d commands, want %d", len(calls), before+1)
		}
		return calls[before]
	}
	patch1 := send("node.set", `{"operation_id":"`+opID(1)+`","expected_version":4,"params":{"settings":{"node_queue_limit":20000,"endpoint_defaults":{"backlog_limit":99,"max_queue_s":60}}}}`)
	if patch1.Name != "node.set" || patch1.Expected != 4 || patch1.Operation.Admin().Kind != admin.OpChangeNodeSettings {
		t.Errorf("command %+v", patch1)
	}
	// the fingerprint, worked out by hand: the canonical settings have the members in
	// order and no spaces
	want := sha256.Sum256([]byte("sidera-manage-v1\x00node.set\x00" +
		`{"settings":{"endpoint_defaults":{"backlog_limit":99,"max_queue_s":60},"node_queue_limit":20000}}` + "\x00" + "4"))
	if patch1.Fingerprint != want {
		t.Errorf("fingerprint %x, want %x", patch1.Fingerprint, want)
	}
	patch2 := send("node.set", `{ "params": {"settings": { "node_queue_limit" : 20000 , "endpoint_defaults" : { "max_queue_s" : 60 , "backlog_limit" : 99 } } } , "expected_version" : 4, "operation_id":"`+opID(2)+`" }`)
	if patch1.Fingerprint != patch2.Fingerprint {
		t.Error("the same patch written in another order has another fingerprint")
	}
	patch3 := send("node.set", `{"operation_id":"`+opID(3)+`","expected_version":4,"params":{"settings":{"node_queue_limit":20001,"endpoint_defaults":{"backlog_limit":99,"max_queue_s":60}}}}`)
	if patch3.Fingerprint == patch1.Fingerprint {
		t.Error("a patch with another value shares a fingerprint")
	}
	ep := send("endpoints.set", `{"operation_id":"`+opID(4)+`","expected_version":7,"params":{"endpoint":"`+addressOf(3)+`","settings":{"backlog_limit":5}}}`)
	if ep.Name != "endpoints.set" || ep.Expected != 7 || ep.Operation.Admin().Kind != admin.OpChangeEndpointSettings {
		t.Errorf("command %+v", ep)
	}
	// An operation that carries no version has the version zero.
	for _, tc := range []struct {
		method, params string
		kind           admin.OpKind
	}{
		{"alerts.ack", `{"alert":"` + strings.Repeat("ab", 16) + `"}`, admin.OpAcknowledgeAlert},
		{"identity.confirm_backup", `{"digest":"` + strings.Repeat("ab", 32) + `"}`, admin.OpConfirmRevocationBackup},
		{"identity.retire", `{"node":"` + ownNode.String() + `"}`, admin.OpRetireIdentity},
		{"clock.clear_rollback", `{}`, admin.OpClearClockRollback},
	} {
		c := send(tc.method, `{"operation_id":"`+opID(9)+`","params":`+tc.params+`}`)
		if c.Name != tc.method || c.Expected != 0 || c.Operation.Admin().Kind != tc.kind {
			t.Errorf("%s: command %+v", tc.method, c)
		}
	}
}

// A retirement is the one request here that stops the daemon: the answer is
// written first and the stop is asked for once, with the name of the method.
func TestTheRetirementStopsTheDaemonAfterTheAnswer(t *testing.T) {
	r := newRig(t)
	r.svc.execute = func(_ context.Context, c engine.ManagementCommand) (engine.StoredResult, error) {
		return engine.StoredResult{Result: []byte(`{"retired":true}`), Stop: c.Name == "identity.retire"}, nil
	}
	if status, out := r.post("clock.clear_rollback", `{"operation_id":"`+opID(1)+`","params":{}}`); status != 200 {
		t.Fatalf("%d %s", status, out)
	}
	if r.stop.count() != 0 {
		t.Fatal("a clear stopped the daemon")
	}
	if status, out := r.post("identity.retire", `{"operation_id":"`+opID(2)+`","params":{"node":"`+ownNode.String()+`"}}`); status != 200 {
		t.Fatalf("%d %s", status, out)
	}
	<-r.stop.ch
	if r.stop.reasons[0] != "identity.retire" {
		t.Errorf("the stop was asked for with %q", r.stop.reasons[0])
	}
}

// The status as the owner reads it, written out by hand: the reminders come with
// their words, every state of the deliveries is listed, and the facts the engine
// does not know are not made up.
func TestStatusAnswersWhatTheEngineKnowsInTheWordsOfTheInterface(t *testing.T) {
	r := newRig(t)
	r.srv.cfg.Version = "1.2.3"
	started := t0.Add(-time.Hour)
	r.svc.status = engine.NodeStatus{
		Node: ownNode, Phase: engine.PhaseRunning, StartedAt: started,
		Reminders:   []engine.ReminderKind{engine.ReminderRevocationBackupUnconfirmed},
		AlertsOpen:  2,
		Clock:       engine.ClockStatus{RollbackPaused: true, PausedSince: t0.Add(-time.Minute), HighWater: t0},
		Certificate: engine.CertificateStatus{NotAfter: t0.Add(48 * time.Hour), Remaining: 48 * time.Hour},
		Capacity:    engine.CapacityStatus{OpenDeliveries: 12, NodeQueueLimit: 10000, OutboundBacklog: 3, DiskFreeBytes: 1 << 30, DiskReserveBytes: 256 << 20},
		Deliveries:  map[delivery.State]int{delivery.StateAccepted: 4, delivery.StateDelivered: 9},
		Endpoints: []engine.EndpointStatus{{
			Address: ident.Address{Node: ownNode, Endpoint: endpointOf(1)}, Kind: adapterreg.KindDirect, Instance: "chat", Status: store.EndpointOpen,
		}},
	}
	status, out := r.post("status", `{}`)
	want := `{"result":{"node":{"id":"` + ownNode.String() + `","version":"1.2.3"},` +
		`"daemon":{"state":"running","started_at":"2026-10-24T08:00:00Z"},` +
		`"reminders":[{"kind":"revocation_backup_unconfirmed","message":"` + reminderMessage[engine.ReminderRevocationBackupUnconfirmed] + `"}],` +
		`"alerts":{"open":3},` + // the two records and the standing pause of the clock
		`"clock":{"rollback_paused":true,"rollback_paused_since":"2026-10-24T08:59:00Z","high_water":"2026-10-24T09:00:00Z"},` +
		`"certificate":{"not_after":"2026-10-26T09:00:00Z","remaining_s":172800},` +
		`"capacity":{"open_deliveries":12,"node_queue_limit":10000,"outbound_backlog":3,"disk_free_bytes":1073741824,"disk_reserve_bytes":268435456},` +
		`"deliveries":{"accepted":4,"closed_unconfirmed":0,"delivered":9,"delivery_unknown":0,"expired_unconfirmed":0,"queued":0,"rejected":0},` +
		`"endpoints":[{"address":"` + addressOf(1) + `","kind":"direct","instance":"chat","state":"open"}]}}`
	if status != 200 || strings.TrimSpace(string(out)) != want {
		t.Errorf("%d\n%s\nwant\n%s", status, out, want)
	}
	if strings.Contains(strings.ToLower(string(out)), "token") {
		t.Error("the status names a token")
	}
}

// The alerts the owner has not acknowledged, oldest first, and with the others on
// request; while the clock is paused the list ends with the alert of the pause,
// which has no identifier to acknowledge.
func TestTheListOfAlertsHoldsTheStandingPauseAndHidesWhatWasAcknowledged(t *testing.T) {
	r := newRig(t)
	seen := &store.AlertAcked{At: t0}
	r.svc.alerts = []store.AlertRecord{
		{ID: store.AlertID{1}, Kind: "disk_low", At: t0.Add(-2 * time.Hour), Detail: "below the reserve", Subject: "state"},
		{ID: store.AlertID{2}, Kind: "evidence_error", At: t0.Add(-time.Hour), Detail: "bad evidence", Acked: seen},
	}
	r.svc.status = engine.NodeStatus{Clock: engine.ClockStatus{RollbackPaused: true, PausedSince: t0}}
	status, out := r.post("alerts.list", `{}`)
	want := `{"result":{"alerts":[` +
		`{"id":"` + (store.AlertID{1}).String() + `","kind":"disk_low","since":"2026-10-24T07:00:00Z","subject":"state","detail":"below the reserve","acknowledged":false},` +
		`{"id":"clock_rollback","kind":"clock_rollback","since":"2026-10-24T09:00:00Z","detail":"the wall clock was set back; nothing that is signed or depends on a deadline is accepted until ` + "`sidera clock clear-rollback`" + `","acknowledged":false}]}}`
	if status != 200 || strings.TrimSpace(string(out)) != want {
		t.Errorf("%d\n%s\nwant\n%s", status, out, want)
	}
	status, out = r.post("alerts.list", `{"params":{"include_acknowledged":true}}`)
	if status != 200 || !strings.Contains(string(out), `"acknowledged":true`) || strings.Count(string(out), `"kind"`) != 3 {
		t.Errorf("with the acknowledged ones: %d %s", status, out)
	}
	r.svc.status = engine.NodeStatus{}
	status, out = r.post("alerts.list", `{}`)
	if status != 200 || strings.Contains(string(out), "clock_rollback") {
		t.Errorf("without a pause: %d %s", status, out)
	}
}

// The pause of the clock is an alert the owner sees in the list, so the status counts
// it among the open ones: the number the status shows is the number of entries the
// list shows as not acknowledged, with and without the pause.
func TestTheStatusCountsTheAlertsTheListShowsAsOpen(t *testing.T) {
	r := newRig(t)
	r.svc.alerts = []store.AlertRecord{{ID: store.AlertID{1}, Kind: "disk_low", At: t0, Detail: "below the reserve"}}
	for _, paused := range []bool{false, true} {
		r.svc.status = engine.NodeStatus{AlertsOpen: 1, Clock: engine.ClockStatus{RollbackPaused: paused, PausedSince: t0}}
		var st struct {
			Result struct {
				Alerts struct {
					Open int `json:"open"`
				} `json:"alerts"`
			} `json:"result"`
		}
		var list struct {
			Result struct {
				Alerts []struct {
					Acknowledged bool `json:"acknowledged"`
				} `json:"alerts"`
			} `json:"result"`
		}
		status, out := r.post("status", `{}`)
		if err := json.Unmarshal(out, &st); status != 200 || err != nil {
			t.Fatalf("status: %d %s (%v)", status, out, err)
		}
		status, out = r.post("alerts.list", `{}`)
		if err := json.Unmarshal(out, &list); status != 200 || err != nil {
			t.Fatalf("alerts.list: %d %s (%v)", status, out, err)
		}
		if want := len(list.Result.Alerts); st.Result.Alerts.Open != want {
			t.Errorf("paused %v: the status counts %d open alerts, the list shows %d", paused, st.Result.Alerts.Open, want)
		}
		if want := map[bool]int{false: 1, true: 2}[paused]; st.Result.Alerts.Open != want {
			t.Errorf("paused %v: %d open alerts, want %d", paused, st.Result.Alerts.Open, want)
		}
	}
}

// The alert of the pause of the clock has no identifier to acknowledge: asked to
// acknowledge it by the name the list shows, the owner is told in so many words that
// the pause is cleared by `clock clear-rollback`, as a refusal that names the alert as
// not held, and nothing reaches the engine.
func TestAcknowledgingTheAlertOfThePauseSaysHowItIsCleared(t *testing.T) {
	r := newRig(t)
	status, out := r.post("alerts.ack", `{"operation_id":"`+opID(1)+`","params":{"alert":"clock_rollback"}}`)
	var body apiwire.Body
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if status != 404 || body.Error.Category != apiwire.CategoryNotFound || body.Error.Code != "alert_unknown" ||
		!strings.Contains(body.Error.Message, "sidera clock clear-rollback") {
		t.Errorf("%d %s; want 404 not_found/alert_unknown whose message names `sidera clock clear-rollback`", status, out)
	}
	if got := r.svc.calls(); len(got) != 0 {
		t.Errorf("the engine was asked %d times to acknowledge an alert that is no record", len(got))
	}
}

// node.show and the export answer from the engine in the words of the interface.
func TestNodeShowAndTheExportAnswerFromTheEngine(t *testing.T) {
	r := newRig(t)
	r.svc.node = config.Node{Version: 3, Settings: config.DefaultNodeSettings()}
	status, out := r.post("node.show", `{}`)
	want := `{"result":{"node":"` + ownNode.String() + `","version":3,"settings":{"reply_permission":true,"node_queue_limit":10000,"disk_reserve_bytes":268435456,` +
		`"node_prompt_turns":8,"archive_retention_days":30,"log_total_bytes":157286400,"endpoint_defaults":{"max_queue_s":1800,"backlog_limit":200}}}}`
	if status != 200 || strings.TrimSpace(string(out)) != want {
		t.Errorf("%d\n%s\nwant\n%s", status, out, want)
	}
	r.svc.export = engine.RevocationExport{Armor: []byte("-----BEGIN SIDERA SELF-REVOCATION-----\nAAAA\n-----END SIDERA SELF-REVOCATION-----\n"), Digest: record.Digest{0xab}}
	status, out = r.post("identity.export_revocation", `{}`)
	wantExport := `{"result":{"armored":"-----BEGIN SIDERA SELF-REVOCATION-----\nAAAA\n-----END SIDERA SELF-REVOCATION-----\n","digest":"ab` + strings.Repeat("00", 31) + `"}}`
	if status != 200 || strings.TrimSpace(string(out)) != wantExport {
		t.Errorf("%d\n%s\nwant\n%s", status, out, wantExport)
	}
	// An export carries no operation identifier: it is a read.
	status, out = r.post("identity.export_revocation", `{"operation_id":"`+opID(1)+`"}`)
	r.wantError(400, "invalid_argument", "malformed_request", status, out)
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("a read reached Execute %d times", n)
	}
}
