package manageapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
)

// timeText is a time as the interface writes one: UTC, to the second.
func timeText(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// reminderMessage is what the owner is told for each reminder. The words are the
// interface's; the engine says which reminder stands.
var reminderMessage = map[engine.ReminderKind]string{
	engine.ReminderRevocationBackupUnconfirmed: "The statement that can retire this node has not been confirmed as kept outside this device. " +
		"Run `sidera identity export-revocation`, keep the copy offline, then run `sidera identity confirm-backup <digest>`.",
}

// states is the order the deliveries are listed in: every state is there, with zero
// where the node holds none, so that a reader that counts the ones with work behind
// them does not have to know which states exist.
var states = []delivery.State{
	delivery.StateQueued, delivery.StateAccepted, delivery.StateDelivered, delivery.StateRejected,
	delivery.StateExpiredUnconfirmed, delivery.StateDeliveryUnknown, delivery.StateClosedUnconfirmed,
}

type statusJSON struct {
	Node struct {
		ID      string `json:"id"`
		Version string `json:"version,omitempty"`
	} `json:"node"`
	Daemon struct {
		State     string `json:"state"`
		StartedAt string `json:"started_at"`
	} `json:"daemon"`
	Reminders []reminderJSON `json:"reminders"`
	Alerts    struct {
		Open int `json:"open"`
	} `json:"alerts"`
	Clock struct {
		RollbackPaused      bool   `json:"rollback_paused"`
		RollbackPausedSince string `json:"rollback_paused_since,omitempty"`
		HighWater           string `json:"high_water,omitempty"`
	} `json:"clock"`
	Certificate struct {
		NotAfter   string `json:"not_after,omitempty"`
		RemainingS int64  `json:"remaining_s"`
	} `json:"certificate"`
	Capacity struct {
		OpenDeliveries   int    `json:"open_deliveries"`
		NodeQueueLimit   int    `json:"node_queue_limit"`
		OutboundBacklog  int    `json:"outbound_backlog"`
		DiskFreeBytes    uint64 `json:"disk_free_bytes"`
		DiskReserveBytes int64  `json:"disk_reserve_bytes"`
	} `json:"capacity"`
	Deliveries map[string]int  `json:"deliveries"`
	Endpoints  []endpointState `json:"endpoints"`
}

type reminderJSON struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type endpointState struct {
	Address  string `json:"address"`
	Kind     string `json:"kind"`
	Instance string `json:"instance,omitempty"`
	State    string `json:"state"`
}

// phaseName is where the node is, for the owner: the phases a read can be answered
// in are the two that run (a stopping engine refuses reads).
func phaseName(p engine.Phase) string { return p.String() }

// status: what the owner is shown of the node. The reminders are first, because a
// reminder is what the owner is to do, and stay until it is done.
var status = &method{
	name: "status", op: admin.Operation{Kind: admin.OpRead},
	plan: func(s *Server, raw json.RawMessage) (*plan, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), read: func(ctx context.Context, svc engine.ManageService) (any, error) {
			st, err := svc.Status(ctx)
			if err != nil {
				return nil, err
			}
			var out statusJSON
			out.Node.ID, out.Node.Version = st.Node.String(), s.cfg.Version
			out.Daemon.State, out.Daemon.StartedAt = phaseName(st.Phase), timeText(st.StartedAt)
			out.Reminders = []reminderJSON{}
			for _, k := range st.Reminders {
				out.Reminders = append(out.Reminders, reminderJSON{Kind: k.String(), Message: reminderMessage[k]})
			}
			out.Alerts.Open = st.AlertsOpen
			out.Clock.RollbackPaused = st.Clock.RollbackPaused
			if !st.Clock.PausedSince.IsZero() {
				out.Clock.RollbackPausedSince = timeText(st.Clock.PausedSince)
			}
			if !st.Clock.HighWater.IsZero() {
				out.Clock.HighWater = timeText(st.Clock.HighWater)
			}
			if !st.Certificate.NotAfter.IsZero() {
				out.Certificate.NotAfter = timeText(st.Certificate.NotAfter)
				out.Certificate.RemainingS = int64(st.Certificate.Remaining / time.Second)
			}
			out.Capacity.OpenDeliveries, out.Capacity.NodeQueueLimit = st.Capacity.OpenDeliveries, st.Capacity.NodeQueueLimit
			out.Capacity.OutboundBacklog = st.Capacity.OutboundBacklog
			out.Capacity.DiskFreeBytes, out.Capacity.DiskReserveBytes = st.Capacity.DiskFreeBytes, st.Capacity.DiskReserveBytes
			out.Deliveries = map[string]int{}
			for _, d := range states {
				out.Deliveries[d.String()] = st.Deliveries[d]
			}
			out.Endpoints = []endpointState{}
			for _, ep := range st.Endpoints {
				out.Endpoints = append(out.Endpoints, endpointState{
					Address: ep.Address.String(), Kind: ep.Kind.String(), Instance: ep.Instance, State: stateName(ep.Status),
				})
			}
			return out, nil
		}}, nil
	},
}

type alertJSON struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Since        string `json:"since"`
	Subject      string `json:"subject,omitempty"`
	Detail       string `json:"detail"`
	Acknowledged bool   `json:"acknowledged"`
}

// kindClockRollback names the alert that a pause of the clock stands for: it is no
// record, it is the pause, and it goes when the owner clears the pause.
const kindClockRollback = "clock_rollback"

// alerts.list: the alerts the owner has not acknowledged, oldest first, and with
// include_acknowledged the others too. While the clock is paused the list ends with
// the alert of the pause, which has no identifier to acknowledge: it is lifted by
// clearing the pause.
var alertsList = &method{
	name: "alerts.list", op: admin.Operation{Kind: admin.OpRead},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			IncludeAcknowledged bool `json:"include_acknowledged,omitempty"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), read: func(ctx context.Context, svc engine.ManageService) (any, error) {
			alerts, err := svc.Alerts(ctx)
			if err != nil {
				return nil, err
			}
			st, err := svc.Status(ctx)
			if err != nil {
				return nil, err
			}
			out := struct {
				Alerts []alertJSON `json:"alerts"`
			}{Alerts: []alertJSON{}}
			for _, a := range alerts {
				if a.Acked != nil && !p.IncludeAcknowledged {
					continue
				}
				out.Alerts = append(out.Alerts, alertJSON{
					ID: a.ID.String(), Kind: a.Kind, Since: timeText(a.At), Subject: a.Subject, Detail: a.Detail, Acknowledged: a.Acked != nil,
				})
			}
			if st.Clock.RollbackPaused {
				since := ""
				if !st.Clock.PausedSince.IsZero() {
					since = timeText(st.Clock.PausedSince)
				}
				out.Alerts = append(out.Alerts, alertJSON{
					ID: kindClockRollback, Kind: kindClockRollback, Since: since,
					Detail: "the wall clock was set back; nothing that is signed or depends on a deadline is accepted until `sidera clock clear-rollback`",
				})
			}
			return out, nil
		}}, nil
	},
}

// alerts.ack: the owner has seen an alert. The identifier is the one the list shows.
var alertsAck = &method{
	name: "alerts.ack", op: admin.Operation{Kind: admin.OpAcknowledgeAlert},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Alert string `json:"alert"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		id, err := store.ParseAlertID(p.Alert)
		if err != nil {
			return nil, apiwire.Malformed("the value is not the identifier of an alert", "params.alert")
		}
		p.Alert = id.String()
		op := engine.AcknowledgeAlertOp(id, func(id store.AlertID) ([]byte, error) {
			return json.Marshal(struct {
				Alert        string `json:"alert"`
				Acknowledged bool   `json:"acknowledged"`
			}{id.String(), true})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}
