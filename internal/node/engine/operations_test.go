package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
)

// The owner's operations on identity, alerts and settings, each through the one
// door. The result of each is made by the interface and passed in as a function;
// the tests word them as the interface does.

func encodeBackup(c engine.BackupConfirmed) ([]byte, error) {
	return json.Marshal(struct {
		ConfirmedAt string `json:"confirmed_at"`
	}{c.At.UTC().Format(time.RFC3339)})
}

func backupCommand(b byte, content string, digest record.Digest) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf(content), Name: "identity.confirm_backup",
		Operation: engine.ConfirmRevocationBackupOp(digest, encodeBackup),
	}
}

func retireCommand(b byte, content string, node ident.NodeID) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf(content), Name: "identity.retire",
		Operation: engine.RetireIdentityOp(node, func() ([]byte, error) {
			return []byte(`{"node":"` + node.String() + `","retired":true,"stopping":true}`), nil
		}),
	}
}

func ackCommand(b byte, content string, id store.AlertID) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf(content), Name: "alerts.ack",
		Operation: engine.AcknowledgeAlertOp(id, func(id store.AlertID) ([]byte, error) {
			return []byte(`{"alert":"` + id.String() + `","acknowledged":true}`), nil
		}),
	}
}

func setNodeSettingsCommand(b byte, content string, expected admin.Version, patch string) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf(content), Expected: expected, Name: "node.set",
		Operation: engine.SetNodeSettingsOp([]byte(patch), func(n config.Node) ([]byte, error) {
			settings, err := n.Settings.WireJSON()
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				Settings json.RawMessage `json:"settings"`
				Version  uint64          `json:"version"`
			}{settings, uint64(n.Version)})
		}),
	}
}

func endpointSetCommand(b byte, content string, expected admin.Version, endpoint ident.EndpointID, patch string) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf(content), Expected: expected, Name: "endpoints.set",
		Operation: engine.SetEndpointSettingsOp(endpoint, []byte(patch), func(c engine.EndpointChange) ([]byte, error) {
			settings, err := c.Settings.WireJSON()
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				Endpoint string          `json:"endpoint"`
				Settings json.RawMessage `json:"settings"`
				Version  uint64          `json:"version"`
			}{c.Endpoint.String(), settings, uint64(c.Version)})
		}),
	}
}

func wantInvalid(t testing.TB, err error, field string, detail engine.Detail) {
	t.Helper()
	var ref *engine.Refusal
	if !errors.As(err, &ref) || ref.Reason != engine.ReasonInvalidArgument || ref.Field != field || ref.Detail != detail {
		t.Fatalf("error %v, want an invalid argument %q with the detail %s", err, field, detail)
	}
}

// The owner confirms that a copy of the self-revocation is kept outside this
// device by naming the digest of the statement the node holds. A digest of another
// statement confirms nothing and writes nothing; the right one lifts the reminder,
// and the confirmation is on disk, where a restart finds it.
func TestTheBackupIsConfirmedOnlyForTheStatementTheNodeHolds(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withKeys())
	e := r.start()
	ctx := context.Background()
	statement := r.keys.SelfRevocation()
	if got := nodeStatusOf(t, e).Reminders; !reflect.DeepEqual(got, []engine.ReminderKind{engine.ReminderRevocationBackupUnconfirmed}) {
		t.Fatalf("reminders of a node whose backup was never confirmed: %v", got)
	}

	before := r.snapshot()
	_, err := e.Execute(ctx, backupCommand(81, "confirm another statement", record.Digest{1}))
	wantInvalid(t, err, "digest", engine.DetailDigestMismatch)
	if !reflect.DeepEqual(before, r.snapshot()) {
		t.Error("a confirmation of another statement wrote to the disk")
	}
	if len(nodeStatusOf(t, e).Reminders) != 1 {
		t.Error("a confirmation of another statement lifted the reminder")
	}

	first, err := e.Execute(ctx, backupCommand(82, "confirm", statement.Digest()))
	if err != nil || first.Replayed || first.Stop {
		t.Fatalf("the confirmation: %+v, %v", first, err)
	}
	if want := `{"confirmed_at":"` + t0.Format(time.RFC3339) + `"}`; string(first.Result) != want {
		t.Errorf("the result is %s, want %s", first.Result, want)
	}
	if got := nodeStatusOf(t, e).Reminders; len(got) != 0 {
		t.Errorf("reminders after the confirmation: %v", got)
	}
	again, err := e.Execute(ctx, backupCommand(82, "confirm", statement.Digest()))
	if err != nil || !again.Replayed || string(again.Result) != string(first.Result) {
		t.Errorf("the confirmation sent again: %+v, %v", again, err)
	}

	r.restart()
	e = r.start()
	if got := nodeStatusOf(t, e).Reminders; len(got) != 0 {
		t.Errorf("reminders after a restart: %v; the confirmation is on disk", got)
	}
}

// A confirmation is for one statement: the one that is on disk for another
// identity says nothing about this one, so the node whose keys were made again
// over the same data directory reminds again.
func TestAConfirmationOfAnotherStatementDoesNotPutTheReminderOut(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withKeys())
	foreign, err := config.Backup{ConfirmedAt: t0, Digest: record.Digest{7}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	r.seedFile("config", "revocation-backup.json", foreign)
	r.restart()
	e := r.start()
	if got := nodeStatusOf(t, e).Reminders; len(got) != 1 {
		t.Errorf("reminders with the confirmation of another statement on disk: %v, want the reminder", got)
	}
}

// The owner retires the identity of the node: the statement the keys hold is kept
// as a revocation under config/, the answer asks for the daemon to stop, and a
// start over what is on disk finds the node retired. The node is named, so that a
// retirement meant for another data directory is refused and changes nothing.
func TestRetiringTheIdentityKeepsTheStatementAndAsksForTheStop(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withKeys())
	e := r.start()
	ctx := context.Background()

	before := r.snapshot()
	_, err := e.Execute(ctx, retireCommand(83, "retire another node", mustNode(t, 9)))
	wantInvalid(t, err, "node", engine.DetailNodeMismatch)
	if !reflect.DeepEqual(before, r.snapshot()) {
		t.Error("a retirement of another node wrote to the disk")
	}
	if r.cfg.RevocationSet().NodeRetired(r.node) {
		t.Fatal("the node is retired after a request that named another node")
	}

	res, err := e.Execute(ctx, retireCommand(84, "retire", r.node))
	if err != nil || res.Replayed || !res.Stop {
		t.Fatalf("the retirement: %+v, %v; want a result that asks for the stop", res, err)
	}
	if !r.cfg.RevocationSet().NodeRetired(r.node) {
		t.Error("the revocations in memory do not hold the retirement")
	}
	// The statement is on disk under the name its digest gives, as the bytes the keys
	// verified.
	statement := r.keys.SelfRevocation()
	onDisk, err := r.mem.ReadFile(path.Join("config", "revocations", statement.Digest().String()+".rec"))
	if err != nil || string(onDisk) != string(statement.Wire()) {
		t.Errorf("config/revocations holds %d bytes (%v), want the statement of the keys", len(onDisk), err)
	}
	// The same request sent again is the stored result and asks for the stop again,
	// so that a daemon that died between the result and the stop is stopped by it.
	again, err := e.Execute(ctx, retireCommand(84, "retire", r.node))
	if err != nil || !again.Replayed || !again.Stop || string(again.Result) != string(res.Result) {
		t.Errorf("the retirement sent again: %+v, %v", again, err)
	}

	// What a start reads: the revocation, and the rule that refuses to run under it.
	r.restart()
	if !r.cfg.RevocationSet().NodeRetired(r.node) {
		t.Fatal("the node is not retired after a restart")
	}
	if err := r.keys.CheckNotRetired(r.cfg.RevocationSet()); err == nil {
		t.Error("the keys accept to run a node that was retired")
	}
}

// An alert is acknowledged by its identifier; one the node does not hold is not
// found. A request under another identifier for an alert that is acknowledged is
// answered like the first.
func TestAcknowledgingAnAlertMarksIt(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	alertEp, alertIn := store.AlertID{0xa1}, store.AlertID{0xa2}
	for _, a := range []store.AlertRecord{
		{ID: alertEp, Kind: "corrupt_record", At: t0, Detail: "an endpoint record", Subject: "endpoints/x"},
		{ID: alertIn, Kind: "corrupt_record", At: t0.Add(time.Second), Detail: "a target record", Subject: "in/x"},
	} {
		r.seedRecord(a)
	}
	e := r.start()
	ctx := context.Background()

	if got := nodeStatusOf(t, e).AlertsOpen; got != 2 {
		t.Errorf("%d alerts open before any was acknowledged, want 2", got)
	}
	_, err := e.Execute(ctx, ackCommand(85, "ack an alert nobody raised", store.AlertID{0xff}))
	wantReason(t, err, engine.ReasonAlertUnknown)

	first, err := e.Execute(ctx, ackCommand(86, "ack the endpoint alert", alertEp))
	if err != nil || first.Replayed || string(first.Result) != `{"alert":"`+alertEp.String()+`","acknowledged":true}` {
		t.Fatalf("the acknowledgement: %+v, %v", first, err)
	}
	if _, err := e.Execute(ctx, ackCommand(87, "ack the delivery alert", alertIn)); err != nil {
		t.Fatal(err)
	}

	alerts, err := e.Alerts(ctx)
	if err != nil || len(alerts) != 2 {
		t.Fatalf("Alerts: %v, %v", alerts, err)
	}
	for _, a := range alerts {
		if a.Acked == nil {
			t.Errorf("alert %s is not acknowledged", a.ID)
		}
	}
	if a := alerts[0]; a.ID != alertEp || a.Acked.Operation != requestKey(86).ID || !a.Acked.At.Equal(t0) {
		t.Errorf("the first alert is %+v, want it acknowledged by the second request at the time of the clock", a)
	}
	if got := nodeStatusOf(t, e).AlertsOpen; got != 0 {
		t.Errorf("%d alerts open after both were acknowledged", got)
	}

	// Under another identifier the same alert is answered the same, and the first
	// acknowledgement stays what it was.
	second, err := e.Execute(ctx, ackCommand(88, "ack it again", alertEp))
	if err != nil || second.Replayed || string(second.Result) != string(first.Result) {
		t.Errorf("a second acknowledgement: %+v, %v", second, err)
	}
	alerts, _ = e.Alerts(ctx)
	if alerts[0].Acked.Operation != requestKey(86).ID {
		t.Errorf("the second acknowledgement took the place of the first: %+v", alerts[0].Acked)
	}
	// And it is on disk.
	r.restart()
	e = r.start()
	if alerts, _ = e.Alerts(ctx); len(alerts) != 2 || alerts[0].Acked == nil || alerts[1].Acked == nil {
		t.Errorf("alerts after a restart: %+v", alerts)
	}
}

// A change of the settings of the node goes through the door like every other
// change of a versioned setting: the version the request names is checked inside
// the change, a patch is merged and judged whole, and the result is kept in the
// file in the replacement that carries the new version.
func TestChangingTheSettingsOfTheNodeIsAVersionedChangeThroughTheDoor(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ctx := context.Background()
	version := r.cfg.Node().Version

	res, err := e.Execute(ctx, setNodeSettingsCommand(91, "raise the queue", version, `{"node_queue_limit":12345}`))
	if err != nil || res.Replayed {
		t.Fatalf("the change: %+v, %v", res, err)
	}
	var got struct {
		Settings struct {
			NodeQueueLimit int  `json:"node_queue_limit"`
			ReplyPerm      bool `json:"reply_permission"`
		} `json:"settings"`
		Version uint64 `json:"version"`
	}
	if err := json.Unmarshal(res.Result, &got); err != nil || got.Settings.NodeQueueLimit != 12345 || !got.Settings.ReplyPerm || got.Version != uint64(version)+1 {
		t.Errorf("the result is %s (%v); want the new queue limit, the other settings kept and the next version", res.Result, err)
	}
	if n := r.cfg.Node(); n.Settings.NodeQueueLimit != 12345 || n.Version != version+1 {
		t.Errorf("the configuration holds %+v", n)
	}
	// The result is kept in node.json, which is where the replay finds it after a
	// restart.
	r.restart()
	e = r.start()
	again, err := e.Execute(ctx, setNodeSettingsCommand(91, "raise the queue", version, `{"node_queue_limit":12345}`))
	if err != nil || !again.Replayed || string(again.Result) != string(res.Result) {
		t.Errorf("the change sent again after a restart: %+v, %v", again, err)
	}

	// The version of a request that was based on an older setting is a conflict, and
	// nothing is written.
	before := r.snapshot()
	_, err = e.Execute(ctx, setNodeSettingsCommand(92, "stale", version, `{"node_queue_limit":20000}`))
	var ce *config.ChangeError
	if !errors.As(err, &ce) || ce.Reason != config.ChangeVersionConflict || ce.Current != version+1 {
		t.Errorf("a stale change: %v, want a version conflict that names the current version %d", err, version+1)
	}
	_, err = e.Execute(ctx, setNodeSettingsCommand(93, "empty", version+1, `{}`))
	if !errors.As(err, &ce) || ce.Reason != config.ChangeNothingToChange {
		t.Errorf("a patch that names nothing: %v", err)
	}
	_, err = e.Execute(ctx, setNodeSettingsCommand(94, "below the minimum", version+1, `{"node_queue_limit":0}`))
	if !errors.As(err, &ce) || ce.Reason != config.ChangeInvalid {
		t.Errorf("a value below the minimum: %v", err)
	}
	if !reflect.DeepEqual(before, r.snapshot()) {
		t.Error("a change that was refused wrote to the disk")
	}
}

// The settings of one endpoint are changed through the same door; the endpoint
// is found by the engine, which knows its instance, and one it does not know is
// not found.
func TestChangingTheSettingsOfAnEndpointFindsItsInstance(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ctx := context.Background()
	a := register(t, e, "a")
	version := r.cfg.Instances().Version

	res, err := e.Execute(ctx, endpointSetCommand(95, "backlog", version, a, `{"backlog_limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"endpoint":"` + a.String() + `","settings":{"backlog_limit":3},"version":` + strconv.FormatUint(uint64(version)+1, 10) + `}`; string(res.Result) != want {
		t.Errorf("the result is %s, want %s", res.Result, want)
	}
	if got := r.cfg.EndpointSettings("chat", a); got.BacklogLimit != 3 {
		t.Errorf("the endpoint's settings are %+v", got)
	}
	// What the owner gave before stays when a patch names the other setting, and the
	// result shows both.
	res, err = e.Execute(ctx, endpointSetCommand(96, "queue", version+1, a, `{"max_queue_s":600}`))
	if err != nil || !strings.Contains(string(res.Result), `"settings":{"max_queue_s":600,"backlog_limit":3}`) {
		t.Errorf("the second change: %s, %v", res.Result, err)
	}
	_, err = e.Execute(ctx, endpointSetCommand(97, "nobody", version+2, mustEndpoint(t, 99), `{"backlog_limit":3}`))
	wantReason(t, err, engine.ReasonEndpointUnknown)
	before := r.snapshot()
	_, err = e.Execute(ctx, endpointSetCommand(98, "invalid", version+2, a, `{"backlog_limit":0}`))
	var ce *config.ChangeError
	if !errors.As(err, &ce) || ce.Reason != config.ChangeInvalid {
		t.Errorf("a backlog limit of zero: %v", err)
	}
	if !reflect.DeepEqual(before, r.snapshot()) {
		t.Error("a change that was refused wrote to the disk")
	}
}
