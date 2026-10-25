package store_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"path"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// base is the instant every fixture is built from. Fixtures never read a clock.
var base = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

// node, ep, person build identifiers from one repeated byte, so that a test
// reads as "node 1, endpoint 2" and the text of an identifier is stable.
func node(i int) ident.NodeID {
	n, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{byte(i)}, ident.NodeIDLen))
	if err != nil {
		panic(err)
	}
	return n
}

func ep(i int) ident.EndpointID {
	e, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{byte(0x10 + i)}, ident.EndpointIDLen))
	if err != nil {
		panic(err)
	}
	return e
}

func addr(n, e int) ident.Address { return ident.Address{Node: node(n), Endpoint: ep(e)} }

// The local node of every fixture is node 1; endpoints 1 to 9 are local.
const local = 1

// recorder is a txn.Observer that keeps the points it is told about and can run
// a callback on each, before the step runs.
type recorder struct {
	mu     sync.Mutex
	points []fsx.Point
	kinds  []txn.Kind
	on     func(p fsx.Point, k txn.Kind)
	sink   *coverageSink
	// inTx counts, for the transaction under way, how often each key was
	// reached in it: the sink's maxInTx is built from it.
	inTx   map[fsx.Key]int
	inTxOf string
}

// coverageSink counts, across the rigs of a test, how often each crash point
// was reached.
type coverageSink struct {
	mu   sync.Mutex
	hits map[fsx.Key]int
	// maxInTx is the most times one transaction reached a key: a write declared
	// as many is covered only when a single transaction made it more than once
	// (the hits of a key over a whole run can come from different transactions).
	maxInTx map[fsx.Key]int
	kind    map[fsx.Key]txn.Kind
	// taken counts, for each crash point and each kind of crash, the crash
	// states the walk of a workflow took there and judged: reaching a point is
	// not crashing at it.
	taken map[fsx.Key]map[string]int
}

func newSink() *coverageSink {
	return &coverageSink{hits: map[fsx.Key]int{}, maxInTx: map[fsx.Key]int{}, kind: map[fsx.Key]txn.Kind{}, taken: map[fsx.Key]map[string]int{}}
}

// took records that the walk took a crash state of the given kind at p.
func (c *coverageSink) took(p fsx.Point, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.taken[p.Key] == nil {
		c.taken[p.Key] = map[string]int{}
	}
	c.taken[p.Key][model]++
}

func (c *coverageSink) add(p fsx.Point, k txn.Kind, inTx int) {
	c.mu.Lock()
	c.hits[p.Key]++
	c.maxInTx[p.Key] = max(c.maxInTx[p.Key], inTx)
	c.kind[p.Key] = k
	c.mu.Unlock()
}

// countInTx counts the arrival at p in its transaction: the count starts again
// when another transaction begins, and after a commit.
func (r *recorder) countInTx(p fsx.Point) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inTx == nil || r.inTxOf != p.Tag.Tx {
		r.inTx, r.inTxOf = map[fsx.Key]int{}, p.Tag.Tx
	}
	r.inTx[p.Key]++
	n := r.inTx[p.Key]
	if p.Step == fsx.StepCommit {
		r.inTx = nil
	}
	return n
}

func (r *recorder) Reached(p fsx.Point, k txn.Kind) {
	if r.sink != nil {
		r.sink.add(p, k, r.countInTx(p))
	}
	r.mu.Lock()
	r.points = append(r.points, p)
	r.kinds = append(r.kinds, k)
	on := r.on
	r.mu.Unlock()
	if on != nil {
		on(p, k)
	}
}

// forget drops the points kept so far.
func (r *recorder) forget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.points, r.kinds = nil, nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.points)
}

// working counts the points of everything but the barrier that txn.Recover ends
// with to make the directories durable: the writes a process that has nothing
// to do still makes are the barrier's alone.
func (r *recorder) working() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.points {
		if p.Tag.Tx != "txn-settle" {
			n++
		}
	}
	return n
}

// Cross-owner declarations that exercise the write functions another package
// calls. The first role of each belongs to a package that is not this one.
var (
	declXAdmit = txn.Decl{Name: "x-admit", Kind: txn.Normal, Writes: []txn.Write{
		{Role: "ids", Op: txn.OpReplace, Optional: true},
		{Role: store.RoleEndpoint, Op: txn.OpReplace, Optional: true},
	}}
	declXClear = txn.Decl{Name: "x-clear", Kind: txn.Normal, Writes: []txn.Write{
		{Role: "clock", Op: txn.OpReplace},
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
	// declXLayout is what the composition root declares for the layout rule.
	declXLayout = txn.Decl{Name: "layout", Kind: txn.Recovery, Writes: []txn.Write{
		{Role: "dir", Op: txn.OpMakeDir, Many: true, Optional: true},
	}}
)

// rig is a Writer and a Store over an in-memory file system, with the state
// directories made.
type rig struct {
	t   testing.TB
	mem *fsxtest.Mem
	tab *txn.Table
	w   *txn.Writer
	st  *store.Store
	rec *recorder
}

func program(t testing.TB) *txn.Table {
	t.Helper()
	tab, err := programTable()
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	return tab
}

func programTable() (*txn.Table, error) {
	decls := append(txn.Internal(), store.Transactions()...)
	decls = append(decls, &declXAdmit, &declXClear, &declXLayout)
	return txn.NewTable(decls...)
}

// txnTable is every crash point the store declares.
func txnTable() ([]txn.Key, error) {
	tab, err := programTable()
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, d := range store.Transactions() {
		mine[d.Name] = true
	}
	var out []txn.Key
	for _, k := range tab.Keys() {
		if mine[k.Tag.Tx] {
			out = append(out, k)
		}
	}
	return out, nil
}

func kindName(k txn.Kind) string {
	if k == txn.Recovery {
		return "recovery"
	}
	return "normal"
}

var setupTag = fsx.Tag{Tx: "setup", Role: "f"}

// newRig returns a rig on an empty disk with the layout in place and the config
// and keys directories the cross-owner declarations write to.
func newRig(t testing.TB) *rig {
	t.Helper()
	mem := fsxtest.NewMem()
	for _, d := range append([]string{"config", "keys"}, store.StateDirs()...) {
		if err := fsx.MakeDir(mem, setupTag, d); err != nil {
			t.Fatalf("setup MakeDir(%q): %v", d, err)
		}
	}
	return openRig(t, mem)
}

// openRig starts a process over a disk: a new Writer and Store, the way a
// restart does, after the leftovers were removed and the directories made
// durable. The points of that start are forgotten, so that a test counts only
// what the call it makes reaches.
func openRig(t testing.TB, mem *fsxtest.Mem) *rig {
	t.Helper()
	r := openRigRaw(t, mem, nil)
	r.restart()
	r.rec.forget()
	return r
}

// openRigRaw starts a process without the step that removes leftovers, so that
// a test can watch that step too.
func openRigRaw(t testing.TB, mem *fsxtest.Mem, sink *coverageSink) *rig {
	t.Helper()
	r := &rig{t: t, mem: mem, tab: program(t), rec: &recorder{sink: sink}}
	r.w = txn.New(mem, r.tab, r.rec)
	r.st = store.New(r.w)
	return r
}

// restart is what the start of a process does first: remove the leftovers.
func (r *rig) restart() {
	r.t.Helper()
	if err := r.w.Recover(); err != nil {
		r.t.Fatalf("Recover: %v", err)
	}
}

// scan scans and fails the test on an error.
func (r *rig) scan() *store.Scan {
	r.t.Helper()
	sc, err := r.st.Scan()
	if err != nil {
		r.t.Fatalf("Scan: %v", err)
	}
	return sc
}

// snapshot reads every file below dir as path to content, skipping debris.
func snapshot(t testing.TB, r fsx.Reader, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	var walk func(d string)
	walk = func(d string) {
		entries, err := r.ReadDir(d)
		if err != nil {
			if pe, ok := err.(*fs.PathError); ok && pe.Err == fs.ErrNotExist {
				return
			}
			t.Fatalf("ReadDir(%q): %v", d, err)
		}
		for _, e := range entries {
			if fsx.IsLeftover(e.Name) {
				continue
			}
			p := path.Join(d, e.Name)
			if e.IsDir {
				walk(p)
				continue
			}
			b, err := r.ReadFile(p)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", p, err)
			}
			got[p] = string(b)
		}
	}
	walk(dir)
	return got
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Fixtures. Each takes a number that makes the message, and so the file, unique.

func msgID(i int) delivery.MessageID { return delivery.MessageID(fmt.Sprintf("msg-%04d", i)) }

func digestOf(i int) delivery.BodyDigest {
	var d delivery.BodyDigest
	for j := range d {
		d[j] = byte(i + j)
	}
	return d
}

func bodyBytes(i int) []byte { return []byte(fmt.Sprintf("body of message %d", i)) }

// outKey is the key of message i from local endpoint 1 to endpoint 2 of node 2
// (a message to another node), or to local endpoint 2 when toLocal.
func outKey(i int, toLocal bool) store.OutKey {
	to := addr(2, 2)
	if toLocal {
		to = addr(local, 2)
	}
	return store.OutKey{Sender: ep(1), Message: msgID(i), Recipients: []ident.Address{to}}
}

// srcOut is a regular source record, queued, with a body.
func srcOut(i int, toLocal bool) store.OutRecord {
	k := outKey(i, toLocal)
	accept := base.Add(time.Duration(i) * time.Minute)
	body := store.BodyRefOf(bodyBytes(i))
	return store.OutRecord{
		Key: k,
		Outbound: envelope.Outbound{
			Message:      k.Message,
			From:         k.Sender,
			Kind:         envelope.KindMessage,
			BodyNonEmpty: true,
			AcceptBefore: accept,
			Recipients: []envelope.Tracked{{
				Address:  k.Recipients[0],
				Delivery: delivery.NewSourceRecord(accept),
			}},
		},
		Envelope: store.OutEnvelope{
			To:          k.Recipients,
			Seq:         map[ident.EndpointID]uint64{k.Recipients[0].Endpoint: uint64(i)},
			SentAt:      base,
			ReportBy:    delivery.ReportBy(accept),
			ContentType: "text/plain",
		},
		Body:       &body,
		BodyDigest: digestOf(i),
	}
}

// tgtKey is the key of message i from node 2 endpoint 3 to local endpoint 2.
func tgtKey(i int) store.TargetKey {
	return store.TargetKey{SenderNode: node(2), Sender: ep(3), Message: msgID(i), Recipient: ep(2)}
}

// tgtIn is a regular target record, accepted, with a body.
func tgtIn(i int) store.InRecord {
	k := tgtKey(i)
	accept := base.Add(time.Duration(i) * time.Minute)
	reportBy := delivery.ReportBy(accept)
	rec, err := delivery.NewTargetRecord(base, accept, reportBy, delivery.DefaultEndpointMaxQueue)
	if err != nil {
		panic(err)
	}
	body := store.BodyRefOf(bodyBytes(i))
	return store.InRecord{
		Key:           k,
		Record:        rec,
		Dedup:         delivery.NewDedupRecord(k.Dedup(), digestOf(i), reportBy),
		Auth:          delivery.Authorization{Kind: delivery.AuthorizationAllow},
		ReplyDecision: reply.Decision{Reason: reply.ReasonDisabled},
		Handout:       store.HandoutFields{ContentType: "text/plain", SentAt: base},
		AcceptedAt:    base,
		Body:          &body,
	}
}

// noticeKey is the key of notice i of the system endpoint to local endpoint 2.
func noticeKey(i int) store.TargetKey {
	return store.TargetKey{SenderNode: node(2), Sender: ident.SystemEndpoint(), Message: msgID(i), Recipient: ep(2)}
}

// noticeIn is tgtIn for a notice of the system endpoint: exempt traffic, which
// holds neither a verdict of the authorization check nor a reply permission.
func noticeIn(i int) store.InRecord {
	in := tgtIn(i)
	in.Key = noticeKey(i)
	accept := base.Add(time.Duration(i) * time.Minute)
	rec, err := delivery.NewTargetNoticeRecord(base, accept, delivery.ReportBy(accept), delivery.DefaultEndpointMaxQueue)
	if err != nil {
		panic(err)
	}
	in.Record = rec
	in.Dedup = delivery.NewDedupRecord(in.Key.Dedup(), digestOf(i), rec.ReportBy)
	in.Auth, in.ReplyDecision = delivery.Authorization{}, reply.Decision{}
	return in
}

// ctlKey is the key of control signal i from node 2 endpoint 3 to local
// endpoint 4, the runtime endpoint.
func ctlKey(i int) store.TargetKey {
	return store.TargetKey{SenderNode: node(2), Sender: ep(3), Message: msgID(i), Recipient: ep(4)}
}

// sessionCtl is a regular control record of a session creation, with an intent.
func sessionCtl(i int, session int) store.CtlRecord {
	k := ctlKey(i)
	accept := base.Add(time.Duration(i) * time.Minute)
	reportBy := delivery.ReportBy(accept)
	return store.CtlRecord{
		Key:             k,
		Record:          delivery.NewTargetControlRecord(accept, reportBy),
		Dedup:           delivery.NewDedupRecord(k.Dedup(), digestOf(i), reportBy),
		Kind:            envelope.KindSessionCreate,
		Preset:          "default",
		SessionEndpoint: ep(session),
		AcceptedAt:      base,
		SentAt:          base,
		Intent: &store.CreateIntent{
			Attempt:  adapterproto.AttemptID("0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6"),
			Endpoint: ep(session),
			State:    store.IntentCreating,
		},
	}
}

func token(i int) []byte { return bytes.Repeat([]byte{byte(0x40 + i)}, 32) }

// session is a session endpoint record created by control record ctlName.
func session(i int, status store.EndpointStatus, createdBy string) store.EndpointRecord {
	r := store.EndpointRecord{
		ID:        ep(i),
		Kind:      adapterreg.KindSession,
		Instance:  "claude",
		Status:    status,
		Token:     token(i),
		Session:   &store.SessionInfo{Preset: "default", ToolAccess: []string{"SIDERA_TOKEN"}},
		CreatedBy: createdBy,
	}
	if status == store.EndpointClosed {
		r.Token = nil
	}
	return r
}

// directEndpoint is an open direct endpoint of an adapter instance.
func directEndpoint(i int, instance string) store.EndpointRecord {
	return store.EndpointRecord{
		ID: ep(i), Kind: adapterreg.KindDirect, Instance: instance,
		Status: store.EndpointOpen, Token: token(i), Declaration: []byte(`{"name":"direct"}`),
	}
}

// equalRecords compares two decoded records field by field with the rules of
// reflect.DeepEqual, after time values are compared by instant.
func equalRecords(a, b any) bool { return reflect.DeepEqual(normalize(a), normalize(b)) }

// normalize returns a copy of v in which every time.Time is its UTC instant
// without monotonic reading, so that DeepEqual compares instants.
func normalize(v any) any {
	return normalizeValue(reflect.ValueOf(v)).Interface()
}

func normalizeValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Struct:
		if t, ok := v.Interface().(time.Time); ok {
			return reflect.ValueOf(t.UTC().Round(0))
		}
		for i := 0; i < v.NumField(); i++ {
			if !v.Type().Field(i).IsExported() {
				return v // an identifier or the like: opaque, compared as it is
			}
		}
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			out.Field(i).Set(normalizeValue(v.Field(i)))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(normalizeValue(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(normalizeValue(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for _, k := range v.MapKeys() {
			out.SetMapIndex(k, normalizeValue(v.MapIndex(k)))
		}
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(normalizeValue(v.Elem()))
		return out
	}
	return v
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) }

// faultyRig is a rig whose file system fails the steps its test names.
type faultyRig struct {
	*rig
	faulty *fsxtest.Faulty
}

// newFaultyRig starts a process over mem through a fault injector. The process
// recovers before the test names a fault, and the points of that start are
// forgotten.
func newFaultyRig(t *testing.T, mem *fsxtest.Mem) *faultyRig {
	t.Helper()
	f := fsxtest.NewFaulty(t, mem)
	r := &rig{t: t, mem: mem, tab: program(t), rec: &recorder{}}
	r.w = txn.New(f, r.tab, r.rec)
	r.st = store.New(r.w)
	r.restart()
	r.rec.forget()
	return &faultyRig{rig: r, faulty: f}
}

// srcOutFinished is a source record whose only delivery was delivered.
func srcOutFinished(i int) store.OutRecord { return finishedOut(i, false) }

// finishedOut is a source record whose only delivery was delivered.
func finishedOut(i int, toLocal bool) store.OutRecord {
	r := srcOut(i, toLocal)
	d, _, err := r.Outbound.Recipients[0].Delivery.Apply(base, delivery.Event{Type: delivery.EventDelivered})
	if err != nil {
		panic(err)
	}
	r.Outbound.Recipients[0].Delivery = d
	r.Body = nil
	return r
}

// adminResult is the result of an administrative operation numbered i.
func adminResult(i int) store.AdminRecord {
	var op admin.OperationID
	op[0], op[15] = byte(i), 1
	var fp admin.Fingerprint
	fp[0] = byte(i)
	return store.AdminRecord{
		Key: admin.RequestKey{ID: op}, Fingerprint: fp, Op: "endpoint.close",
		Result: []byte(`{"closed":true}`), At: base, RetainUntil: base.Add(store.AdminResultRetention),
	}
}

// seed writes records onto the disk durably, outside any transaction: the state
// of the disk before the thing under test happens. A record that cannot be
// encoded is a mistake of the test.
func seed(t testing.TB, mem *fsxtest.Mem, recs ...store.Record) {
	t.Helper()
	for _, rec := range recs {
		data, err := store.EncodeRecord(rec)
		if err != nil {
			t.Fatalf("seed %T: %v", rec, err)
		}
		if err := fsx.Replace(mem, setupTag, path.Join(dirOf(rec), rec.Name()), data, fsx.ModePrivate); err != nil {
			t.Fatalf("seed %T: %v", rec, err)
		}
	}
}

// seedBodies writes body files durably.
func seedBodies(t testing.TB, mem *fsxtest.Mem, bodies ...[]byte) {
	t.Helper()
	for _, b := range bodies {
		if err := fsx.Replace(mem, setupTag, path.Join(store.BodiesDir, store.BodyRefOf(b).Name()), b, fsx.ModePrivate); err != nil {
			t.Fatalf("seed body: %v", err)
		}
	}
}

// seedRaw writes a file under dir with the given name and content.
func seedRaw(t testing.TB, mem *fsxtest.Mem, dir, name string, content []byte) {
	t.Helper()
	if err := fsx.Replace(mem, setupTag, path.Join(dir, name), content, fsx.ModePrivate); err != nil {
		t.Fatalf("seed %s/%s: %v", dir, name, err)
	}
}

// damageBody flips a bit in the last byte of a file, which is in its body.
func damageBody(t testing.TB, mem *fsxtest.Mem, p string) {
	t.Helper()
	b, err := mem.ReadFile(p)
	if err != nil {
		t.Fatalf("damageBody: %v", err)
	}
	b = append([]byte(nil), b...)
	b[len(b)-2] ^= 0x01
	if err := fsx.Replace(mem, setupTag, p, b, fsx.ModePrivate); err != nil {
		t.Fatalf("damageBody: %v", err)
	}
}

// damageHeader flips a bit in the key of the first line of a file.
func damageHeader(t testing.TB, mem *fsxtest.Mem, p string) {
	t.Helper()
	b, err := mem.ReadFile(p)
	if err != nil {
		t.Fatalf("damageHeader: %v", err)
	}
	b = append([]byte(nil), b...)
	nl := bytes.IndexByte(b, '\n')
	b[nl-2] ^= 0x01
	if err := fsx.Replace(mem, setupTag, p, b, fsx.ModePrivate); err != nil {
		t.Fatalf("damageHeader: %v", err)
	}
}

// inRev returns a target record as it is on disk at the revision.
func inRev(i int, rev uint64) store.InRecord {
	r := tgtIn(i)
	r.Rev = rev
	return r
}

// inFor is a target record for message i addressed to local endpoint e.
func inFor(i, e int, rev uint64) store.InRecord {
	r := tgtIn(i)
	r.Key.Recipient = ep(e)
	r.Dedup = delivery.NewDedupRecord(r.Key.Dedup(), digestOf(i), r.Record.ReportBy)
	r.Rev = rev
	return r
}

// bodyRef is the reference of the body of message i.
func bodyRef(i int) *store.BodyRef {
	b := store.BodyRefOf(bodyBytes(i))
	return &b
}

func envelopeKindSessionClose() envelope.Kind { return envelope.KindSessionClose }

// applied0 is a control record's delivery after its reply was committed.
func applied0(r delivery.Record) delivery.Record {
	out, _, err := r.Apply(base, delivery.Event{Type: delivery.EventReplyCommitted})
	if err != nil {
		panic(err)
	}
	return out
}

// derivedIn is the delivery of the initial message of control signal ctl to the
// session endpoint it created: same sender and message as the control record,
// addressed to the session.
func derivedIn(ctl, session int, rev uint64) store.InRecord {
	r := tgtIn(ctl)
	r.Key.Recipient = ep(session)
	r.Dedup = delivery.NewDedupRecord(r.Key.Dedup(), digestOf(ctl), r.Record.ReportBy)
	r.Rev = rev
	r.Derived = &store.DerivedInfo{From: ep(4), AuthRequest: []byte(`{"stage":"intake"}`)}
	return r
}

// zeroRec is a record file name no record has.
const zeroRec = "0000000000000000000000000000000000000000000000000000000000000000.rec"

// fixedLease is the page identifier of every lease a fixture takes.
var fixedLease = func() delivery.LeaseID {
	id, err := delivery.ParseLeaseID("00112233445566778899aabbccddeeff")
	if err != nil {
		panic(err)
	}
	return id
}()

// leasedRecord is the target record r after its only chunk was handed out at
// now: the record Handout makes, never one assembled by hand.
func leasedRecord(r delivery.Record, now time.Time) delivery.Record {
	out, res, err := r.Handout(now, delivery.AuthorizationAllow, 1, 1, fixedLease)
	if err != nil || res.Action != delivery.HandoutGranted {
		panic(fmt.Sprintf("Handout: %+v %v", res, err))
	}
	return out
}

// deliveredRecord is the record r after the delivery rules delivered it: the
// acknowledgement of its only chunk for a message, the committed reply for a
// control record.
func deliveredRecord(r delivery.Record) delivery.Record {
	if r.Control {
		return applied0(r)
	}
	if r.Lease.Chunks == 0 {
		r = leasedRecord(r, base)
	}
	out, verdict, _, err := r.Ack(base, delivery.Ack{Part: 1, Lease: r.Lease.IDs[0], TokenMatches: true})
	if err != nil || verdict != delivery.AckCompleted {
		panic(fmt.Sprintf("Ack: %v %v", verdict, err))
	}
	return out
}

// lostRecord is the record that replaces a delivery record lost to storage
// corruption at at, with the feedback its construction owes, from the delivery
// package's own constructor.
func lostRecord(side delivery.Side, control bool, at time.Time) (delivery.Record, []delivery.Feedback) {
	r, o, err := delivery.NewLostRecord(side, control, at)
	if err != nil {
		panic(err)
	}
	return r, o.Feedback
}

// lostOutAt is the replacement of the source record under k: one lost delivery
// per recipient and the feedback each owes.
func lostOutAt(k store.OutKey, at time.Time) store.OutRecord {
	rec := store.OutRecord{Key: k, Lost: &store.LostOut{DiscoveredAt: at}}
	for _, to := range k.Recipients {
		r, feedback := lostRecord(delivery.SideSource, false, at)
		rec.Lost.Deliveries = append(rec.Lost.Deliveries, r)
		for _, f := range feedback {
			rec.Feedback = append(rec.Feedback, delivery.Item{
				Ref:      delivery.DeliveryRef{Sender: ident.Address{Node: node(local), Endpoint: k.Sender}, Message: k.Message, Recipient: to},
				Feedback: f, At: at,
			})
		}
	}
	return rec
}

// lostInAt is the replacement of the target record under k, with the dedup
// record that stands in for the lost one.
func lostInAt(k store.TargetKey, at time.Time) store.InRecord {
	r, _ := lostRecord(delivery.SideTarget, false, at)
	return store.InRecord{Key: k, Lost: true, DiscoveredAt: at, Record: r, Dedup: delivery.NewLostDedupRecord(k.Dedup(), at)}
}

// lostCtlAt is the replacement of the control record under k.
func lostCtlAt(k store.TargetKey, at time.Time) store.CtlRecord {
	r, _ := lostRecord(delivery.SideTarget, true, at)
	return store.CtlRecord{Key: k, Lost: true, DiscoveredAt: at, Record: r, Dedup: delivery.NewLostDedupRecord(k.Dedup(), at)}
}

// misnamed is the content of a quarantined file whose first line parses, is of a
// kind its directory holds and names a key that is valid, but whose key does not
// hash to the name the file had: the first line of the record `claimed`, damaged
// in its body, in the file that `under` had. Nothing in it can be trusted to
// belong to that file.
func misnamed(t testing.TB, claimed, under store.Record, id store.AlertID) (name string, data []byte) {
	t.Helper()
	data = append([]byte(nil), mustEncode(t, claimed)...)
	data[len(data)-2] ^= 0x01
	return corruptName(under, id), data
}
