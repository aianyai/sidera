package inproc

import (
	"bytes"
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

func transactionBenchmarkStoreMaintenanceCases() []transactionBenchmarkCase {
	return []transactionBenchmarkCase{
		{name: "instance-drain", prepare: prepareTransactionMaintenanceInstanceDrain},
		{name: "feedback-form", prepare: prepareTransactionMaintenanceFeedbackForm},
		{name: "feedback-clear", prepare: prepareTransactionMaintenanceFeedbackClear},
		{name: "checkpoint-put", prepare: prepareTransactionMaintenanceCheckpointPut},
		{name: "checkpoint-drop", prepare: prepareTransactionMaintenanceCheckpointDrop},
		{name: "alert-raise", prepare: prepareTransactionMaintenanceAlertRaise},
		{name: "archive-summaries", prepare: prepareTransactionMaintenanceArchiveSummaries},
		{name: "archive-sweep", prepare: prepareTransactionMaintenanceArchiveSweep},
		{name: "archive-expire", prepare: prepareTransactionMaintenanceArchiveExpire},
		{name: "alert-ack", prepare: prepareTransactionMaintenanceAlertAck},
		{name: "daemon-stop", prepare: prepareTransactionMaintenanceDaemonStop},
		{name: "quarantine", prepare: prepareTransactionMaintenanceQuarantine},
		{name: "orphan-gc", prepare: prepareTransactionMaintenanceOrphanGC},
		{name: "orphan-gc-endpoint", prepare: prepareTransactionMaintenanceOrphanGCEndpoint},
		{name: "derived-replay", prepare: prepareTransactionMaintenanceDerivedReplay},
		{name: "ctl-effect-replay", prepare: prepareTransactionMaintenanceCtlEffectReplay},
	}
}

func newTransactionMaintenanceStore(b *testing.B, observer txn.Observer) (fsx.FS, *store.Store) {
	b.Helper()
	fs, w := newTransactionBenchmarkWriter(b, observer)
	harness.MakeLayout(b, fs)
	return fs, store.New(w)
}

func transactionMaintenanceEndpoint(b *testing.B, status store.EndpointStatus) store.EndpointRecord {
	b.Helper()
	e := newTransactionBenchmarkStoreRecords(b).endpoint
	e.Status = status
	if status == store.EndpointClosed {
		e.Token = nil
	}
	return e
}

func transactionMaintenanceCheckpoint(b *testing.B) store.CheckpointRecord {
	return store.CheckpointRecord{
		Key:   store.CheckpointKey{Scope: store.CheckpointScopeSession, ID: transactionMaintenanceEndpoint(b, store.EndpointClosed).ID.String()},
		State: []byte(`{"cursor":1}`),
	}
}

func transactionMaintenanceAlert() store.AlertRecord {
	return store.AlertRecord{ID: store.AlertID{31, 1}, Kind: "benchmark_warning", At: harness.DefaultStart,
		Subject: "benchmark", Detail: "One persisted owner warning."}
}

func transactionMaintenanceAdmin(op string) store.AdminRecord {
	return store.AdminRecord{
		Key: admin.RequestKey{ID: admin.OperationID{31, 1}}, Fingerprint: admin.Fingerprint{31}, Op: op,
		Result: []byte(`{"changed":true}`), At: harness.DefaultStart,
		RetainUntil: harness.DefaultStart.Add(store.AdminResultRetention),
	}
}

// Recovery inputs use the public record format on the real disk. No measured
// operation is replaced by a fixture-owned transaction sequence.
func transactionMaintenanceSeed(b *testing.B, fs fsx.FS, dir string, record store.Record) {
	b.Helper()
	data, err := store.EncodeRecord(record)
	if err != nil {
		b.Fatal(err)
	}
	if err := fsx.Replace(fs, transactionBenchmarkSetupTag, path.Join(dir, record.Name()), data, fsx.ModePrivate); err != nil {
		b.Fatal(err)
	}
}

func transactionMaintenanceScan(b *testing.B, st *store.Store) *store.Scan {
	b.Helper()
	sc, err := st.Scan()
	if err != nil {
		b.Fatal(err)
	}
	if len(sc.Damaged) != 0 || len(sc.Unknown) != 0 {
		b.Fatalf("unexpected fixture damage or unknown files: %+v %+v", sc.Damaged, sc.Unknown)
	}
	return sc
}

func transactionMaintenanceStored(fs fsx.Reader, dir string, want store.Record) error {
	data, err := fs.ReadFile(path.Join(dir, want.Name()))
	if err != nil {
		return err
	}
	got, damage := store.DecodeRecord(dir, want.Name(), data)
	if damage != nil {
		return fmt.Errorf("stored %s: %+v", want.Name(), damage)
	}
	actual, err := store.EncodeRecord(got)
	if err != nil {
		return err
	}
	expected, err := store.EncodeRecord(want)
	if err == nil && !bytes.Equal(actual, expected) {
		return fmt.Errorf("stored %s differs from the owner input", want.Name())
	}
	return err
}

func transactionMaintenanceAbsent(fs fsx.Reader, dir, name string) error {
	exists, err := fsx.Exists(fs, path.Join(dir, name))
	if err == nil && exists {
		return fmt.Errorf("%s/%s was retained", dir, name)
	}
	return err
}

func prepareTransactionMaintenanceInstanceDrain(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	e := transactionMaintenanceEndpoint(b, store.EndpointOpen)
	if err := st.EndpointUpdate(e); err != nil {
		b.Fatal(err)
	}
	e.Status, e.Token = store.EndpointClosed, nil
	return transactionBenchmarkFixture{
		invoke: func() error { return st.InstanceDrain([]store.EndpointRecord{e}, store.DrainBatch{}) },
		verify: func() error { return transactionMaintenanceStored(fs, store.EndpointsDir, e) },
	}
}

func transactionMaintenanceNotice(b *testing.B, covers []store.CoverRef) ([]byte, store.InRecord) {
	b.Helper()
	body := []byte("One feedback notice.")
	ref := store.BodyRefOf(body)
	key := store.TargetKey{SenderNode: harness.NodeID(), Sender: ident.SystemEndpoint(), Message: "maintenance-notice",
		Recipient: transactionMaintenanceEndpoint(b, store.EndpointOpen).ID}
	accept := harness.DefaultStart.Add(time.Minute)
	rec, err := delivery.NewTargetNoticeRecord(harness.DefaultStart, accept, delivery.ReportBy(accept), delivery.DefaultEndpointMaxQueue)
	if err != nil {
		b.Fatal(err)
	}
	return body, store.InRecord{
		Key: key, Record: rec, Dedup: delivery.NewDedupRecord(key.Dedup(), delivery.BodyDigest(ref.Digest), rec.ReportBy),
		AcceptedAt: harness.DefaultStart, Body: &ref, Covers: covers,
		Handout: store.HandoutFields{ContentType: "text/plain", SentAt: harness.DefaultStart},
	}
}

func prepareTransactionMaintenanceFeedbackForm(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	body, notice := transactionMaintenanceNotice(b, nil)
	return transactionBenchmarkFixture{
		invoke: func() error { return st.FeedbackForm(body, notice) },
		verify: func() error {
			if err := transactionMaintenanceStored(fs, store.InDir, notice); err != nil {
				return err
			}
			got, err := st.ReadBody(notice.Name(), *notice.Body)
			if err == nil && !bytes.Equal(got, body) {
				return fmt.Errorf("feedback body differs from input")
			}
			return err
		},
	}
}

func prepareTransactionMaintenanceFeedbackClear(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	e := transactionMaintenanceEndpoint(b, store.EndpointOpen)
	lost := store.NewOutLostRecord(store.OutLostKey{Sender: e.ID, Damaged: strings.Repeat("a", 64)}, harness.NodeID(), harness.DefaultStart)
	transactionMaintenanceSeed(b, fs, store.OutDir, lost)
	body, notice := transactionMaintenanceNotice(b, []store.CoverRef{store.CoverOf(lost.Name(), lost.Feedback[0])})
	if err := st.FeedbackForm(body, notice); err != nil {
		b.Fatal(err)
	}
	work := store.OutLostReplays(transactionMaintenanceScan(b, st))
	if len(work) != 1 {
		b.Fatalf("feedback preparation produced %d replay records", len(work))
	}
	if len(work[0].Feedback) != 0 {
		b.Fatal("feedback replay did not clear the covered item")
	}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.OutLostClear(work) },
		verify: func() error { return transactionMaintenanceStored(fs, store.OutDir, work[0]) },
	}
}

func prepareTransactionMaintenanceCheckpointPut(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	c := transactionMaintenanceCheckpoint(b)
	return transactionBenchmarkFixture{
		invoke: func() error { return st.CheckpointPut(c) },
		verify: func() error { return transactionMaintenanceStored(fs, store.CheckpointsDir, c) },
	}
}

func prepareTransactionMaintenanceCheckpointDrop(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	c := transactionMaintenanceCheckpoint(b)
	if err := st.CheckpointPut(c); err != nil {
		b.Fatal(err)
	}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.CheckpointDrop([]store.CheckpointKey{c.Key}) },
		verify: func() error { return transactionMaintenanceAbsent(fs, store.CheckpointsDir, c.Name()) },
	}
}

func prepareTransactionMaintenanceAlertRaise(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	a := transactionMaintenanceAlert()
	return transactionBenchmarkFixture{
		invoke: func() error { return st.AlertRaise(a) },
		verify: func() error { return transactionMaintenanceStored(fs, store.AlertsDir, a) },
	}
}

func prepareTransactionMaintenanceArchiveSweep(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	r := transactionMaintenanceAdmin("daemon.stop")
	if err := st.DaemonStop(r); err != nil {
		b.Fatal(err)
	}
	plan := store.PlanSweep(transactionMaintenanceScan(b, st), r.RetainUntil)
	if len(plan.Records) != 1 {
		b.Fatalf("sweep preparation produced %d records", len(plan.Records))
	}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.ArchiveSweep(plan.Chunk()) },
		verify: func() error { return transactionMaintenanceAbsent(fs, store.AdminDir, r.Name()) },
	}
}

func prepareTransactionMaintenanceAlertAck(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	a := transactionMaintenanceAlert()
	if err := st.AlertRaise(a); err != nil {
		b.Fatal(err)
	}
	r := transactionMaintenanceAdmin("alert.ack")
	a.Acked = &store.AlertAcked{At: harness.DefaultStart.Add(time.Minute), Operation: r.Key.ID}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.AlertAck(store.AlertAckInput{Alert: a, Result: r}) },
		verify: func() error {
			if err := transactionMaintenanceStored(fs, store.AlertsDir, a); err != nil {
				return err
			}
			return transactionMaintenanceStored(fs, store.AdminDir, r)
		},
	}
}

func prepareTransactionMaintenanceDaemonStop(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	r := transactionMaintenanceAdmin("daemon.stop")
	return transactionBenchmarkFixture{
		invoke: func() error { return st.DaemonStop(r) },
		verify: func() error { return transactionMaintenanceStored(fs, store.AdminDir, r) },
	}
}

func prepareTransactionMaintenanceQuarantine(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	c := transactionMaintenanceCheckpoint(b)
	if err := st.CheckpointPut(c); err != nil {
		b.Fatal(err)
	}
	p := path.Join(store.CheckpointsDir, c.Name())
	data, err := fs.ReadFile(p)
	if err != nil {
		b.Fatal(err)
	}
	// Keep the original first line and change a body byte without its checksum.
	data[len(data)-2] ^= 1
	if err := fsx.Replace(fs, transactionBenchmarkSetupTag, p, data, fsx.ModePrivate); err != nil {
		b.Fatal(err)
	}
	sc, err := st.Scan()
	if err != nil {
		b.Fatal(err)
	}
	if len(sc.Damaged) != 1 {
		b.Fatalf("quarantine preparation produced %d damaged files", len(sc.Damaged))
	}
	alert := store.QuarantineAlert{Kind: "corrupt_record", At: harness.DefaultStart, Subject: p, Detail: "Damaged checkpoint."}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.Quarantine(store.Quarantine{File: sc.Damaged[0], Alert: alert, Scan: sc}) },
		verify: func() error {
			final, err := st.Scan()
			if err == nil && (len(final.Damaged) != 0 || len(final.Checkpoints) != 0 || len(final.Alerts) != 1 || final.Alerts[0].Subject != p || final.Alerts[0].Kind != alert.Kind) {
				return fmt.Errorf("quarantine did not remove the checkpoint and retain its alert")
			}
			return err
		},
	}
}

func prepareTransactionMaintenanceOrphanGC(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	_, notice := transactionMaintenanceNotice(b, nil)
	name := store.BodyFileName(notice.Name())
	if err := fsx.Replace(fs, transactionBenchmarkSetupTag, path.Join(store.BodiesDir, name), []byte("One orphan body."), fsx.ModePrivate); err != nil {
		b.Fatal(err)
	}
	work := store.OrphanBodies(transactionMaintenanceScan(b, st))
	if len(work) != 1 {
		b.Fatalf("orphan preparation produced %d bodies", len(work))
	}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.OrphanGC(work) },
		verify: func() error { return transactionMaintenanceAbsent(fs, store.BodiesDir, name) },
	}
}

func prepareTransactionMaintenanceOrphanGCEndpoint(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	e := newTransactionBenchmarkStoreRecords(b).creatingEndpoint
	transactionMaintenanceSeed(b, fs, store.EndpointsDir, e)
	work := store.OrphanEndpoints(transactionMaintenanceScan(b, st))
	if len(work) != 1 {
		b.Fatalf("orphan preparation produced %d endpoints", len(work))
	}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.OrphanGCEndpoint(work) },
		verify: func() error { return transactionMaintenanceAbsent(fs, store.EndpointsDir, e.Name()) },
	}
}

// One expired target supplies an owner-planned summary; neither the archive
// contents nor its checksum is manufactured by the fixture.
func transactionMaintenanceArchiveInput(b *testing.B, st *store.Store) (time.Time, []store.ArchiveSummary) {
	b.Helper()
	input := newTransactionBenchmarkStoreRecords(b)
	if err := st.Deliver(input.body, []store.InRecord{input.in}); err != nil {
		b.Fatal(err)
	}
	settled := input.in
	at := settled.Record.ReportBy.Add(30 * 24 * time.Hour)
	var err error
	settled.Record, _, err = settled.Record.Tick(at)
	if err != nil {
		b.Fatal(err)
	}
	if !settled.Record.State.IsTerminal() {
		b.Fatal("archive preparation did not settle its target")
	}
	settled.Body = nil
	if err := st.Tick(store.TickBatch{In: []store.InRecord{settled}}); err != nil {
		b.Fatal(err)
	}
	plan := store.PlanSweep(transactionMaintenanceScan(b, st), at)
	if len(plan.Summaries) != 1 || len(plan.Records) != 1 {
		b.Fatalf("archive preparation produced %d summaries and %d records", len(plan.Summaries), len(plan.Records))
	}
	return at, plan.Summaries
}

func prepareTransactionMaintenanceArchiveSummaries(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	_, st := newTransactionMaintenanceStore(b, observer)
	at, summaries := transactionMaintenanceArchiveInput(b, st)
	return transactionBenchmarkFixture{
		invoke: func() error { return st.ArchiveSummaries(at, summaries) },
		verify: func() error {
			got, err := st.ReadArchive(store.ArchiveDay(at))
			if err != nil {
				return err
			}
			if len(got.Summaries) != 1 || got.Dropped != 0 {
				return fmt.Errorf("archive has %d summaries and %d damaged lines", len(got.Summaries), got.Dropped)
			}
			actual, want := got.Summaries[0], summaries[0]
			if actual.Key != want.Key || actual.Kind != want.Kind || actual.State != want.State || !actual.At.Equal(want.At) || actual.Size != want.Size || actual.Digest != want.Digest {
				return fmt.Errorf("archive summary differs from the sweep plan")
			}
			return nil
		},
	}
}

func prepareTransactionMaintenanceArchiveExpire(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	at, summaries := transactionMaintenanceArchiveInput(b, st)
	if err := st.ArchiveSummaries(at, summaries); err != nil {
		b.Fatal(err)
	}
	work := store.ExpiredArchiveFiles(transactionMaintenanceScan(b, st).Archive, at.Add(48*time.Hour), 24*time.Hour)
	if len(work) != 1 {
		b.Fatalf("expiry preparation produced %d archive files", len(work))
	}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.ArchiveExpire(work) },
		verify: func() error { return transactionMaintenanceAbsent(fs, store.ArchiveDir, work[0]) },
	}
}

func prepareTransactionMaintenanceDerivedReplay(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	input := newTransactionBenchmarkStoreRecords(b)
	transactionMaintenanceSeed(b, fs, store.EndpointsDir, input.creatingEndpoint)
	transactionMaintenanceSeed(b, fs, store.CtlDir, input.ctl)
	if err := fsx.Replace(fs, transactionBenchmarkSetupTag, path.Join(store.BodiesDir, store.BodyFileName(input.derived.Name())), input.body, fsx.ModePrivate); err != nil {
		b.Fatal(err)
	}
	work := store.DerivedReplays(transactionMaintenanceScan(b, st))
	if len(work) != 1 || work[0].Name() != input.ctl.Name() || store.DerivedKey(work[0]) != input.derived.Key {
		b.Fatal("derived preparation did not leave the expected missing initial delivery")
	}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.DerivedReplay([]store.InRecord{input.derived}) },
		verify: func() error {
			if err := transactionMaintenanceStored(fs, store.InDir, input.derived); err != nil {
				return err
			}
			body, err := st.ReadBody(input.derived.Name(), *input.derived.Body)
			if err != nil {
				return err
			}
			if !bytes.Equal(body, input.body) {
				return fmt.Errorf("replayed initial message body differs from input")
			}
			sc, err := st.Scan()
			if err == nil && len(store.DerivedReplays(sc)) != 0 {
				return fmt.Errorf("derived replay retained its recovery work")
			}
			return err
		},
	}
}

func prepareTransactionMaintenanceCtlEffectReplay(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	fs, st := newTransactionMaintenanceStore(b, observer)
	input := newTransactionBenchmarkStoreRecords(b)
	if err := st.ControlIntake(input.body, &input.creatingEndpoint, input.ctl, &input.derived); err != nil {
		b.Fatal(err)
	}
	// The reply was committed while the prepared endpoint still says creating.
	// This is the interrupted control-result state the owner replays at startup.
	ctl := input.ctl
	ctl.Intent.State, ctl.Intent.NativeRef = store.IntentCreated, "benchmark-native-session"
	var err error
	ctl.Record, _, err = ctl.Record.Apply(harness.DefaultStart, delivery.Event{Type: delivery.EventReplyCommitted})
	if err != nil {
		b.Fatal(err)
	}
	ctl.Dedup, err = ctl.Dedup.WithReply([]byte("benchmark-control-reply"))
	if err != nil {
		b.Fatal(err)
	}
	transactionMaintenanceSeed(b, fs, store.CtlDir, ctl)
	work := store.EffectReplays(transactionMaintenanceScan(b, st))
	if len(work) != 1 || work[0].Status != store.EndpointOpen || work[0].Session == nil || work[0].Session.NativeRef != ctl.Intent.NativeRef {
		b.Fatal("effect preparation did not leave the expected opening of a created session")
	}
	return transactionBenchmarkFixture{
		invoke: func() error { return st.CtlEffectReplay(work) },
		verify: func() error {
			if err := transactionMaintenanceStored(fs, store.EndpointsDir, work[0]); err != nil {
				return err
			}
			sc, err := st.Scan()
			if err == nil && len(store.EffectReplays(sc)) != 0 {
				return fmt.Errorf("control-effect replay retained its recovery work")
			}
			return err
		},
	}
}
