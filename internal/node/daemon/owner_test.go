package daemon_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/ident"
)

// The settings of the node, shown and changed through the whole daemon: the version
// that `node.show` gives is the one `node.set` names, a stale one is a conflict with
// the current version in the answer, a patch is judged whole, and what was changed
// is in force and on disk after a restart.
func TestTheSettingsOfTheNodeAreShownAndChangedThroughTheManagementInterface(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	d := started(t, c)

	show := func() (version uint64, settings map[string]any) {
		t.Helper()
		status, body := manage(t, c.Home, "node.show", map[string]any{})
		if status != 200 {
			t.Fatalf("node.show answered %d: %s", status, body)
		}
		var env struct {
			Result struct {
				Node     string         `json:"node"`
				Version  uint64         `json:"version"`
				Settings map[string]any `json:"settings"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &env); err != nil || env.Result.Node != nodeOf(t, c).String() {
			t.Fatalf("node.show: %s (%v)", body, err)
		}
		return env.Result.Version, env.Result.Settings
	}
	version, settings := show()
	if version != 1 || settings["node_queue_limit"] != float64(10000) || settings["reply_permission"] != true {
		t.Fatalf("a node that was just made shows version %d and %v", version, settings)
	}

	set := func(op int, expected uint64, patch string) (int, []byte) {
		return manage(t, c.Home, "node.set", map[string]any{
			"operation_id": opID(op), "expected_version": expected, "params": map[string]any{"settings": json.RawMessage(patch)},
		})
	}
	status, body := set(1, version, `{"node_queue_limit":20000,"endpoint_defaults":{"backlog_limit":99}}`)
	changed := answerOf(t, status, body)
	if !strings.Contains(string(changed.Result), `"node_queue_limit":20000`) || !strings.Contains(string(changed.Result), `"backlog_limit":99`) ||
		!strings.Contains(string(changed.Result), `"max_queue_s":1800`) || !strings.Contains(string(changed.Result), `"version":2`) {
		t.Errorf("the result of the change: %s", changed.Result)
	}
	// the same request again is the stored result; the same identifier for another
	// patch is refused
	status, body = set(1, version, `{"endpoint_defaults":{"backlog_limit":99},"node_queue_limit":20000}`)
	if again := answerOf(t, status, body); !again.Replayed || string(again.Result) != string(changed.Result) {
		t.Errorf("the change written in another order and sent again: %s", body)
	}
	status, body = set(1, version, `{"node_queue_limit":30000}`)
	if status != 409 || !strings.Contains(string(body), "operation_id_reused") {
		t.Errorf("the identifier of another patch: %d %s", status, body)
	}
	// a stale version names the current one
	status, body = set(2, version, `{"node_queue_limit":30000}`)
	if status != 409 || !strings.Contains(string(body), `"version_conflict"`) || !strings.Contains(string(body), `"current_version":2`) {
		t.Errorf("a stale version: %d %s", status, body)
	}
	// what is refused is refused whole, with the path of the value
	for _, tc := range []struct {
		name, patch, code string
	}{
		{"a patch that names nothing", `{}`, "empty_patch"},
		{"a value below the minimum", `{"node_queue_limit":0}`, "config_invalid"},
		{"a setting that does not exist", `{"nothing":1}`, "config_invalid"},
		{"a reserve that does not cover the queue", `{"node_queue_limit":10000000}`, "config_invalid"},
		{"a patch that is not an object", `5`, "config_invalid"},
	} {
		status, body = set(3, 2, tc.patch)
		if status != 400 || !strings.Contains(string(body), `"`+tc.code+`"`) {
			t.Errorf("%s: %d %s; want 400 %s", tc.name, status, body, tc.code)
		}
	}
	if v, s := show(); v != 2 || s["node_queue_limit"] != float64(20000) {
		t.Errorf("after the refused changes node.show says version %d and %v", v, s)
	}

	// on disk: a restart shows the same
	d.RequestStop("restart")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	started(t, c)
	if v, s := show(); v != 2 || s["node_queue_limit"] != float64(20000) {
		t.Errorf("after a restart node.show says version %d and %v", v, s)
	}
}

// The settings of an endpoint are changed by its address; the version is that of the
// instances; an endpoint the node does not know, or one of another node, is not
// found.
func TestTheSettingsOfAnEndpointAreChangedThroughTheManagementInterface(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	ep := endpointRecord(t, 0x21, adapterreg.KindDirect)
	seedHome(t, c.Home, ep)
	node := nodeOf(t, c)
	addr := ident.Address{Node: node, Endpoint: ep.ID}.String()
	started(t, c)

	set := func(op int, expected uint64, endpoint string, patch string) (int, []byte) {
		return manage(t, c.Home, "endpoints.set", map[string]any{
			"operation_id": opID(op), "expected_version": expected,
			"params": map[string]any{"endpoint": endpoint, "settings": json.RawMessage(patch)},
		})
	}
	status, body := set(1, 1, addr, `{"backlog_limit":7}`)
	got := answerOf(t, status, body)
	if want := `{"endpoint":"` + addr + `","settings":{"backlog_limit":7},"version":2}`; string(got.Result) != want {
		t.Errorf("the result is %s, want %s", got.Result, want)
	}
	status, body = set(2, 2, addr, `{"max_queue_s":600}`)
	got = answerOf(t, status, body)
	if want := `{"endpoint":"` + addr + `","settings":{"max_queue_s":600,"backlog_limit":7},"version":3}`; string(got.Result) != want {
		t.Errorf("the second result is %s, want %s", got.Result, want)
	}
	status, body = set(3, 3, ident.Address{Node: node, Endpoint: endpointID(t, 0x7e)}.String(), `{"backlog_limit":7}`)
	if status != 404 || !strings.Contains(string(body), "endpoint_unknown") {
		t.Errorf("an endpoint the node does not have: %d %s", status, body)
	}
	status, body = set(4, 3, addr, `{"backlog_limit":0}`)
	if status != 400 || !strings.Contains(string(body), "config_invalid") {
		t.Errorf("a backlog limit of zero: %d %s", status, body)
	}
	status, body = set(5, 1, addr, `{"backlog_limit":8}`)
	if status != 409 || !strings.Contains(string(body), `"current_version":3`) {
		t.Errorf("a stale version: %d %s", status, body)
	}
}

// The alerts of the node as the owner reads and acknowledges them: the list shows
// what is not acknowledged, oldest first, an acknowledgement is by identifier and is
// answered the same when sent again, an identifier the node does not hold is not
// found, and the status counts what is open.
func TestAlertsAreListedAndAcknowledgedThroughTheManagementInterface(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	d := started(t, c)
	ctx := context.Background()
	first, err := d.Engine().RaiseAlert(ctx, "disk_low", "state", "the volume is below the reserve")
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	second, err := d.Engine().RaiseAlert(ctx, "version_untested", "chat", "an adapter newer than the ones tried")
	if err != nil {
		t.Fatal(err)
	}

	list := func(all bool) []struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		Acknowledged bool   `json:"acknowledged"`
	} {
		t.Helper()
		params := map[string]any{}
		if all {
			params["include_acknowledged"] = true
		}
		status, body := manage(t, c.Home, "alerts.list", map[string]any{"params": params})
		if status != 200 {
			t.Fatalf("alerts.list answered %d: %s", status, body)
		}
		var env struct {
			Result struct {
				Alerts []struct {
					ID           string `json:"id"`
					Kind         string `json:"kind"`
					Acknowledged bool   `json:"acknowledged"`
				} `json:"alerts"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		return env.Result.Alerts
	}
	got := list(false)
	if len(got) != 2 || got[0].ID != first.String() || got[0].Kind != "disk_low" || got[1].ID != second.String() {
		t.Fatalf("the alerts are %+v, want the two oldest first", got)
	}
	openAlerts := func() float64 {
		t.Helper()
		_, body := manage(t, c.Home, "status", map[string]any{})
		var env struct {
			Result struct {
				Alerts struct {
					Open float64 `json:"open"`
				} `json:"alerts"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		return env.Result.Alerts.Open
	}
	if openAlerts() != 2 {
		t.Errorf("status counts %v open alerts, want 2", openAlerts())
	}

	ack := func(op int, alert string) (int, []byte) {
		return manage(t, c.Home, "alerts.ack", map[string]any{"operation_id": opID(op), "params": map[string]any{"alert": alert}})
	}
	status, body := ack(1, first.String())
	acked := answerOf(t, status, body)
	if want := `{"alert":"` + first.String() + `","acknowledged":true}`; string(acked.Result) != want {
		t.Errorf("the result of the acknowledgement is %s, want %s", acked.Result, want)
	}
	if got := list(false); len(got) != 1 || got[0].ID != second.String() {
		t.Errorf("after the acknowledgement the list is %+v", got)
	}
	if got := list(true); len(got) != 2 || !got[0].Acknowledged || got[1].Acknowledged {
		t.Errorf("with the acknowledged ones the list is %+v", got)
	}
	if openAlerts() != 1 {
		t.Errorf("status counts %v open alerts, want 1", openAlerts())
	}
	status, body = ack(1, first.String())
	if again := answerOf(t, status, body); !again.Replayed || string(again.Result) != string(acked.Result) {
		t.Errorf("the acknowledgement sent again: %s", body)
	}
	status, body = ack(2, strings.Repeat("f", 32))
	if status != 404 || !strings.Contains(string(body), "alert_unknown") {
		t.Errorf("an alert the node does not hold: %d %s; want 404 alert_unknown", status, body)
	}
	status, body = ack(3, "clock_rollback")
	if status != 404 || !strings.Contains(string(body), "alert_unknown") || !strings.Contains(string(body), "sidera clock clear-rollback") {
		t.Errorf("the alert of the pause: %d %s; want 404 alert_unknown that says the pause is cleared by `sidera clock clear-rollback`", status, body)
	}
	status, body = ack(4, "disk_low")
	if status != 400 || !strings.Contains(string(body), "malformed_request") {
		t.Errorf("an identifier that is not one: %d %s", status, body)
	}
}

// The status of the node as the owner reads it, from the whole daemon: who it is, how
// it runs, the certificate with its thirty days, the room with the decided limits and
// every state of the deliveries.
func TestStatusAnswersTheStateOfARunningNode(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	c.Version = "9.9.9-test"
	started(t, c)
	status, body := manage(t, c.Home, "status", map[string]any{})
	if status != 200 {
		t.Fatalf("status answered %d: %s", status, body)
	}
	var env struct {
		Result struct {
			Node   struct{ ID, Version string } `json:"node"`
			Daemon struct {
				State     string `json:"state"`
				StartedAt string `json:"started_at"`
			} `json:"daemon"`
			Clock struct {
				RollbackPaused bool `json:"rollback_paused"`
			} `json:"clock"`
			Certificate struct {
				NotAfter   string `json:"not_after"`
				RemainingS int64  `json:"remaining_s"`
			} `json:"certificate"`
			Capacity struct {
				NodeQueueLimit   int   `json:"node_queue_limit"`
				DiskReserveBytes int64 `json:"disk_reserve_bytes"`
				DiskFreeBytes    int64 `json:"disk_free_bytes"`
			} `json:"capacity"`
			Deliveries map[string]int `json:"deliveries"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	r := env.Result
	if r.Node.ID != nodeOf(t, c).String() || r.Node.Version != "9.9.9-test" || r.Daemon.State != "running" || r.Daemon.StartedAt != t0.Format(time.RFC3339) {
		t.Errorf("who and how: %+v %+v", r.Node, r.Daemon)
	}
	if r.Clock.RollbackPaused {
		t.Error("a node that has just started is paused")
	}
	if r.Certificate.NotAfter != t0.Add(30*24*time.Hour).Format(time.RFC3339) || r.Certificate.RemainingS != 30*24*3600 {
		t.Errorf("certificate %+v, want thirty days from the start", r.Certificate)
	}
	if r.Capacity.NodeQueueLimit != 10000 || r.Capacity.DiskReserveBytes != 256<<20 || r.Capacity.DiskFreeBytes <= 0 {
		t.Errorf("capacity %+v", r.Capacity)
	}
	if len(r.Deliveries) != 7 {
		t.Errorf("deliveries %v, want every state listed", r.Deliveries)
	}
	for state, n := range r.Deliveries {
		if n != 0 {
			t.Errorf("a node with no traffic holds %d deliveries %s", n, state)
		}
	}
}
