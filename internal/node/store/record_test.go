package store_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// goldenWant is the record each golden vector stands for, built from the
// fixtures: the vector is the text, this is the meaning.
func goldenWant(t *testing.T, name string) store.Record {
	t.Helper()
	switch name {
	case "seq":
		return store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{addr(2, 2): 5}}
	case "alert":
		var id store.AlertID
		copy(id[:], []byte{0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
		var op admin.OperationID
		copy(op[:], []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef})
		return store.AlertRecord{
			ID: id, Kind: "corrupt_record", At: base,
			Detail: "state/in/x.rec: body checksum mismatch", Subject: "state/in/x.rec",
			Acked: &store.AlertAcked{At: base.Add(30 * time.Minute), Operation: op},
		}
	case "checkpoint":
		return store.CheckpointRecord{
			Key:   store.CheckpointKey{Scope: "attempt", ID: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6"},
			State: []byte(`{"cursor":7}`),
		}
	case "admin":
		var op admin.OperationID
		copy(op[:], []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef})
		var fp admin.Fingerprint
		for i := range fp {
			fp[i] = 0xab
		}
		return store.AdminRecord{
			Key: admin.RequestKey{ID: op}, Fingerprint: fp, Op: "endpoint.close",
			Result: []byte(`{"closed":true}`), At: base, RetainUntil: base.Add(store.AdminResultRetention),
		}
	case "attempts":
		return store.AttemptsRecord{
			Endpoint: ep(5),
			Attempts: []handoff.Attempt{{At: base.Add(-10 * time.Minute), Outcome: handoff.AttemptDelivered, Kind: handoff.PromptKindFirst}},
			Blocked:  handoff.BlockedReport{Cause: handoff.CauseAdapterOffline, Since: base.Add(-5 * time.Minute)},
			Pending: store.Pending{Event: store.PendingNotified, Items: []store.PendingItem{
				{Key: tgtKey(1), Rev: 3, AcceptedAt: base},
			}},
		}
	case "endpoint":
		return session(5, store.EndpointCreating, strings.TrimSuffix(ctlKey(1).FileName(store.KindCtl), ".rec"))
	case "in":
		r := tgtIn(1)
		r.Rev = 1
		return r
	case "out":
		return srcOut(1, false)
	case "ctl":
		return sessionCtl(1, 5)
	}
	t.Fatalf("no record for the golden vector %q", name)
	return nil
}

func TestRecordGoldenVectors(t *testing.T) {
	for _, g := range goldenRecords {
		t.Run(g.name, func(t *testing.T) {
			want := goldenWant(t, g.name)
			if want.Name() != g.hash {
				t.Errorf("Name() = %q, want %q", want.Name(), g.hash)
			}
			got, err := store.EncodeRecord(want)
			if err != nil {
				t.Fatalf("EncodeRecord: %v", err)
			}
			if string(got) != g.file {
				t.Errorf("EncodeRecord:\n got %q\nwant %q", got, g.file)
			}
			rec, dmg := store.DecodeRecord(g.dir, g.hash, []byte(g.file))
			if dmg != nil {
				t.Fatalf("DecodeRecord: %+v", dmg)
			}
			if !equalRecords(rec, want) {
				t.Errorf("DecodeRecord:\n got %#v\nwant %#v", rec, want)
			}
		})
	}
}

// frameOf builds a record file for a body written out as text, with the
// checksum of the standard library: for the cases where the body is the thing
// under test and the frame must be right.
func frameOf(kind string, key, body string) []byte {
	crc := crc32.Checksum([]byte(body), crc32.MakeTable(crc32.Castagnoli))
	return []byte(fmt.Sprintf("sidera-record 1 %s %08x %d %s\n%s", kind, crc, len(body),
		base64.RawURLEncoding.EncodeToString([]byte(key)), body))
}

func nameOf(kind, key string) string {
	h := sha256.Sum256([]byte(kind + " " + key))
	return hex.EncodeToString(h[:]) + ".rec"
}

func golden(t *testing.T, name string) (dir, key, body string) {
	t.Helper()
	for _, g := range goldenRecords {
		if g.name != name {
			continue
		}
		first := strings.IndexByte(g.file, '\n')
		fields := strings.Split(g.file[:first], " ")
		raw, err := base64.RawURLEncoding.DecodeString(fields[5])
		if err != nil {
			t.Fatal(err)
		}
		return g.dir, string(raw), g.file[first+1:]
	}
	t.Fatalf("no golden vector %q", name)
	return
}

// Each rule of the validation of a record file, in the order of the steps, with
// the class of damage it gives. The first four are about the first line and the
// file name and make the key untrustworthy; the others leave it readable.
func TestDecodeRecordStepsAndClasses(t *testing.T) {
	dir, key, body := golden(t, "in")
	good := frameOf("in", key, body)
	goodName := nameOf("in", key)
	if _, dmg := store.DecodeRecord(dir, goodName, good); dmg != nil {
		t.Fatalf("the vector itself does not decode: %+v", dmg)
	}
	ctlDir, ckey, cbody := golden(t, "ctl")

	for _, c := range []struct {
		name   string
		dir    string
		file   string
		data   []byte
		class  store.DamageClass
		reason store.DamageReason
	}{
		{"step 2: a kind the directory does not hold", store.InDir, nameOf("ctl", ckey), frameOf("ctl", ckey, cbody), store.HeaderUnreadable, store.ReasonKindNotAllowed},
		{"step 2: a kind of no directory", store.InDir, nameOf("clock", "clock"), frameOf("clock", "clock", "{}"), store.HeaderUnreadable, store.ReasonKindNotAllowed},
		{"step 2: out-ctl does not go in the target directory", store.InDir, nameOf("out-ctl", key), frameOf("out-ctl", key, body), store.HeaderUnreadable, store.ReasonKindNotAllowed},
		{"step 3: a file name that is not the hash of kind and key", dir, strings.Repeat("0", 64) + ".rec", good, store.HeaderUnreadable, store.ReasonNameMismatch},
		{"step 3: a file name that hashes the key without the kind", dir, func() string { h := sha256.Sum256([]byte(key)); return hex.EncodeToString(h[:]) + ".rec" }(), good, store.HeaderUnreadable, store.ReasonNameMismatch},
		{"step 3: a name without the extension", dir, strings.TrimSuffix(goodName, ".rec"), good, store.HeaderUnreadable, store.ReasonNameMismatch},
		{"step 3: a name in capitals", dir, strings.ToUpper(goodName[:64]) + ".rec", good, store.HeaderUnreadable, store.ReasonNameMismatch},
		{"step 4: a key that does not parse as a key of the kind", dir, nameOf("in", "not a key"), frameOf("in", "not a key", "{}"), store.HeaderUnreadable, store.ReasonKeyInvalid},
		{"step 5: a body cut short", dir, goodName, good[:len(good)-5], store.BodyDamaged, store.ReasonLengthMismatch},
		{"step 6: a flipped bit in the body", dir, goodName, flip(good, len(good)-10), store.BodyDamaged, store.ReasonChecksumMismatch},
		{"step 7: a body that is not JSON", dir, goodName, frameOf("in", key, "not json"), store.BodyDamaged, store.ReasonBodyInvalid},
		{"step 7: an empty body", dir, goodName, frameOf("in", key, ""), store.BodyDamaged, store.ReasonBodyInvalid},
		{"the control record in the wrong directory fails at the kind", ctlDir, nameOf("in", key), frameOf("in", key, body), store.HeaderUnreadable, store.ReasonKindNotAllowed},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec, dmg := store.DecodeRecord(c.dir, c.file, c.data)
			if dmg == nil {
				t.Fatalf("accepted, as %#v", rec)
			}
			if dmg.Class != c.class || dmg.Reason != c.reason {
				t.Errorf("damage = class %d reason %d (%s), want class %d reason %d", dmg.Class, dmg.Reason, dmg.Detail, c.class, c.reason)
			}
			if dmg.Class == store.BodyDamaged && (dmg.Kind != "in" || string(dmg.Key) != key) {
				t.Errorf("a body damage carries the verified kind and key, got %q %q", dmg.Kind, dmg.Key)
			}
		})
	}
}

func flip(b []byte, i int) []byte {
	out := append([]byte(nil), b...)
	out[i] ^= 0x01
	return out
}

// The body rules of step 7. Each row edits the body of a golden vector so that
// it breaks one rule, and gives the frame a right checksum, so that nothing but
// the body rule can reject it. Canonical means: the encoding that EncodeRecord
// produces for what the body decodes to.
func TestDecodeRecordBodyRules(t *testing.T) {
	type edit struct {
		name   string
		vector string
		old    string
		new    string
	}
	rules := []edit{
		{"insignificant whitespace is not canonical", "seq", `{"floors":`, `{ "floors":`},
		{"an escaped character that need not be escaped is not canonical", "alert", `"corrupt_record"`, `"corrupt` + string(rune(0x5c)) + `u005frecord"`},
		{"an unknown field", "seq", `{"floors"`, `{"extra":1,"floors"`},
		{"a duplicate key", "seq", `{"floors":{`, `{"floors":{},"floors":{`},
		{"null is no value", "seq", `"floors":{`, `"floors":null,"x":{`},
		{"a time with an offset instead of Z", "alert", `"2026-10-24T09:00:00Z"`, `"2026-10-24T11:00:00+02:00"`},
		{"a time with trailing zeros in the fraction", "alert", `"2026-10-24T09:00:00Z"`, `"2026-10-24T09:00:00.000Z"`},
		{"a time that is not a time", "alert", `"2026-10-24T09:00:00Z"`, `"yesterday"`},
		{"a floor of zero", "seq", `:5}}`, `:0}}`},
		{"a floor key that is not an address", "seq", `node1qgpq`, `nodeXqgpq`},
		{"an alert kind in capitals", "alert", `"corrupt_record"`, `"Corrupt_Record"`},
		{"an alert acknowledged with a zero operation", "alert", `"operation":"0123456789abcdef0123456789abcdef"`, `"operation":"00000000000000000000000000000000"`},
		{"a checkpoint state that is not an object", "checkpoint", `{"state":{"cursor":7}}`, `{"state":[7]}`},
		{"a checkpoint state with whitespace", "checkpoint", `{"cursor":7}`, `{ "cursor":7}`},
		{"a fingerprint of the wrong length", "admin", strings.Repeat("ab", 32), strings.Repeat("ab", 31)},
		{"a fingerprint in capitals", "admin", strings.Repeat("ab", 32), strings.Repeat("AB", 32)},
		{"an admin retention before the result", "admin", `"retain_until":"2026-10-31T09:00:00Z"`, `"retain_until":"2026-10-23T09:00:00Z"`},
		{"an attempt outcome that is not one", "attempts", `"delivered"`, `"delivered2"`},
		{"a prompt kind that is not one", "attempts", `"kind":"first"`, `"kind":"third"`},
		{"a pending event that is not one", "attempts", `"event":"notified"`, `"event":"told"`},
		{"a pending item key that is not an in key", "attempts", `"key":"node1`, `"key":"nodeX`},
		{"a pending item of revision zero", "attempts", `"rev":3`, `"rev":0`},
		{"an endpoint status that is not one", "endpoint", `"status":"creating"`, `"status":"half"`},
		{"an endpoint kind that is not one", "endpoint", `"kind":"session"`, `"kind":"robot"`},
		{"an endpoint token of the wrong length", "endpoint", `"token":"RUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUU"`, `"token":"RUVF"`},
		{"an open endpoint without a token", "endpoint", `"status":"creating","token":"RUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUU",`, `"status":"open",`},
		{"a closed endpoint that still has a token", "endpoint", `"status":"creating"`, `"status":"closed"`},
		{"a session endpoint with created_by that is not a hash", "endpoint", `"created_by":"`, `"created_by":"x`},
		{"an in record of revision zero", "in", `"rev":1`, `"rev":0`},
		{"an in record without a revision", "in", `"rev":1,`, ``},
		{"an in record without its record", "in", `"record":{"side":"target","state":"accepted","accept_before":"2026-10-24T09:01:00Z","report_by":"2026-10-24T12:03:00Z","backlog_deadline":"2026-10-24T09:30:00Z"},`, ``},
		{"a delivery on the source side in an in record", "in", `"side":"target"`, `"side":"source"`},
		{"a state that is not a state", "in", `"state":"accepted"`, `"state":"accepted2"`},
		{"the invalid state", "in", `"state":"accepted"`, `"state":"invalid"`},
		{"an authorization kind that is not one", "in", `"kind":"allow"`, `"kind":"maybe"`},
		{"a reply reason that is not one", "in", `"reason":"disabled"`, `"reason":"because"`},
		{"a digest of the wrong length", "in", `"digest":"0102`, `"digest":"010`},
		{"a body reference of the wrong digest length", "in", `"sha256":"ea8c`, `"sha256":"ea8`},
		{"a body reference of a negative length", "in", `"len":17`, `"len":-1`},
		{"a body reference past the limit of a body", "in", `"len":17`, `"len":99999999`},
		{"a dedup record with no digest", "in", `"digest":"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",`, ``},
		{"an out kind that is not an envelope kind", "out", `"kind":"message"`, `"kind":"shout"`},
		{"an out kind that is a control signal in a message file", "out", `"kind":"message"`, `"kind":"control.session.close"`},
		{"an out record without recipients", "out", `"recipients":[{"address":"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc","delivery":{"side":"source","state":"queued","accept_before":"2026-10-24T09:01:00Z","report_by":"2026-10-24T12:03:00Z"}}],`, ``},
		{"an out recipient that is not an address", "out", `"address":"node1`, `"address":"nodeX`},
		{"a sequence number of zero", "out", `"seq":1}`, `"seq":0}`},
		{"sequence numbers twice for one endpoint", "out", `"seq":[{"endpoint":"ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc","seq":1}]`, `"seq":[{"endpoint":"ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc","seq":1},{"endpoint":"ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc","seq":2}]`},
		{"a source delivery on the target side in an out record", "out", `"side":"source"`, `"side":"target"`},
		{"an out record whose to is not the key's recipients", "out", `"to":["node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc"]`, `"to":["node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zvf3xycnzvf3xycnzvf3xycnzvl5u4cd"]`},
		{"a control record of a kind that is not a control signal", "ctl", `"kind":"control.session.create"`, `"kind":"message"`},
		{"a control record whose delivery is not a control record", "ctl", `"control":true,`, ``},
		{"an intent state that is not one", "ctl", `"state":"creating"`, `"state":"started"`},
		{"an intent attempt that is not an attempt id", "ctl", `"attempt":"0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6"`, `"attempt":"nope"`},
		{"a created intent without a native reference", "ctl", `"state":"creating"}`, `"state":"created"}`},
	}
	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			dir, key, body := golden(t, r.vector)
			if !strings.Contains(body, r.old) {
				t.Fatalf("the vector %q does not contain %q: the row is stale", r.vector, r.old)
			}
			edited := strings.Replace(body, r.old, r.new, 1)
			kind := strings.Fields(string(frameHead(t, r.vector)))[2]
			rec, dmg := store.DecodeRecord(dir, nameOf(kind, key), frameOf(kind, key, edited))
			if dmg == nil {
				t.Fatalf("accepted, as %#v", rec)
			}
			if dmg.Class != store.BodyDamaged || dmg.Reason != store.ReasonBodyInvalid {
				t.Errorf("damage = class %d reason %d (%s), want a body damage of an invalid body", dmg.Class, dmg.Reason, dmg.Detail)
			}
		})
	}
}

func frameHead(t *testing.T, vector string) []byte {
	t.Helper()
	for _, g := range goldenRecords {
		if g.name == vector {
			return []byte(g.file[:strings.IndexByte(g.file, '\n')])
		}
	}
	t.Fatalf("no vector %q", vector)
	return nil
}

// The shapes: a record is regular or lost, never a mixture.
func TestLostShapesAreExclusive(t *testing.T) {
	_, key, body := golden(t, "in")
	lost := `{"lost":true,"discovered_at":"2026-10-24T10:00:00Z","record":{"side":"target","state":"delivery_unknown","accept_before":"2026-10-24T09:01:00Z","report_by":"2026-10-24T12:03:00Z"},"dedup":{"digest":"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20","retain_until":"2026-10-24T12:03:00Z"}}`
	if _, dmg := store.DecodeRecord(store.InDir, nameOf("in", key), frameOf("in", key, lost)); dmg != nil {
		t.Fatalf("a lost in record: %+v", dmg)
	}
	for name, mixed := range map[string]string{
		"a lost record with a revision":       strings.Replace(lost, `{"lost":true,`, `{"rev":1,"lost":true,`, 1),
		"a lost record with an authorization": strings.Replace(lost, `"dedup"`, `"auth":{"kind":"allow"},"dedup"`, 1),
		"a lost record with a body":           strings.Replace(lost, `"dedup"`, `"body":{"sha256":"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc","len":17},"dedup"`, 1),
		"a lost record without a discovery":   strings.Replace(lost, `"discovered_at":"2026-10-24T10:00:00Z",`, ``, 1),
		"a regular record with a discovery":   strings.Replace(body, `{"rev":1,`, `{"rev":1,"discovered_at":"2026-10-24T10:00:00Z",`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, dmg := store.DecodeRecord(store.InDir, nameOf("in", key), frameOf("in", key, mixed)); dmg == nil {
				t.Error("accepted")
			}
		})
	}
}

// Every record the store can write comes back the same, byte for byte when
// written again. The records cover both shapes, every optional part and the
// states a record passes through.
func TestRecordsRoundTrip(t *testing.T) {
	for name, rec := range roundTripRecords(t) {
		t.Run(name, func(t *testing.T) {
			data, err := store.EncodeRecord(rec)
			if err != nil {
				t.Fatalf("EncodeRecord: %v", err)
			}
			dir := dirOf(rec)
			got, dmg := store.DecodeRecord(dir, rec.Name(), data)
			if dmg != nil {
				t.Fatalf("DecodeRecord: %+v\n%s", dmg, data)
			}
			if !equalRecords(got, rec) {
				t.Errorf("round trip:\n got %#v\nwant %#v", got, rec)
			}
			again, err := store.EncodeRecord(got)
			if err != nil || !bytes.Equal(again, data) {
				t.Errorf("a decoded record does not encode to the same bytes: %v", err)
			}
			if len(data) > store.MaxHeaderLen+store.MaxBodyLen {
				t.Errorf("a record of %d bytes is over the limits", len(data))
			}
		})
	}
}

func dirOf(rec store.Record) string {
	switch r := rec.(type) {
	case store.OutRecord:
		return store.OutDir
	case store.InRecord:
		return store.InDir
	case store.CtlRecord:
		return store.CtlDir
	case store.EndpointRecord:
		return store.EndpointsDir
	case store.AttemptsRecord:
		return store.AttemptsDir
	case store.CheckpointRecord:
		return store.CheckpointsDir
	case store.AdminRecord:
		return store.AdminDir
	case store.AlertRecord:
		return store.AlertsDir
	case store.SeqRecord:
		return store.SeqDir
	default:
		panic(fmt.Sprintf("unknown record %T", r))
	}
}

func roundTripRecords(t *testing.T) map[string]store.Record {
	t.Helper()
	recs := map[string]store.Record{}

	// Source records.
	recs["out queued"] = srcOut(1, false)
	released := srcOut(2, true)
	released.Body = nil
	recs["out with the body released"] = released
	idem := srcOut(3, false)
	idem.Idempotency = &store.Idempotency{Key: "idem-1", RetainUntil: base.Add(24 * time.Hour)}
	idem.Envelope.ReplyTo = msgID(1)
	idem.Envelope.Thread = "thread-1"
	recs["out with an idempotency key, a reply-to and a thread"] = idem
	fb := srcOut(4, false)
	fb.Feedback = []delivery.Item{
		{Ref: delivery.DeliveryRef{Sender: addr(local, 1), Message: msgID(4), Recipient: addr(2, 2)},
			Feedback: delivery.Feedback{Kind: delivery.KindRejected, Reject: delivery.RejectEndpointClosed, State: delivery.StateRejected}, At: base},
		{Ref: delivery.DeliveryRef{Sender: addr(local, 1), Message: msgID(4), Recipient: addr(2, 2)},
			Feedback: delivery.Feedback{Kind: delivery.KindDeliveryBlocked, Cause: delivery.CauseAdapterOffline}, At: base.Add(time.Minute)},
	}
	d, _, _ := fb.Outbound.Recipients[0].Delivery.Apply(base, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed})
	fb.Outbound.Recipients[0].Delivery = d
	fb.Outbound.Recipients[0].Arrival = envelope.Arrival{ArrivedAt: base.Add(time.Minute)}
	recs["out with pending feedback and an arrival"] = fb
	two := srcOut(5, false)
	two.Key.Recipients = []ident.Address{addr(2, 2), addr(3, 4)}
	two.Outbound.Recipients = append(two.Outbound.Recipients, envelope.Tracked{Address: addr(3, 4), Delivery: delivery.NewSourceRecord(two.Outbound.AcceptBefore)})
	two.Envelope.To = two.Key.Recipients
	two.Envelope.Seq[ep(4)] = 9
	recs["out with two recipients"] = two
	ctl := srcOut(6, false)
	ctl.Key.Control = true
	ctl.Outbound.Kind = envelope.KindSessionCreate
	ctl.Envelope.Preset = "default"
	ctl.ControlReply = []byte("a control reply record, kept as it was received")
	ctl.Outbound.ControlReplyDigest = [32]byte{7}
	derived := envelope.Tracked{Address: addr(2, 6), Delivery: delivery.NewSourceRecord(ctl.Outbound.AcceptBefore)}
	ctl.Outbound.Derived = &derived
	recs["out of a session creation with a derived delivery"] = ctl
	recs["out lost"] = store.OutRecord{
		Key: outKey(7, false),
		Lost: &store.LostOut{DiscoveredAt: base.Add(time.Hour), Deliveries: []delivery.Record{
			{Side: delivery.SideSource, State: delivery.StateDeliveryUnknown, AcceptBefore: base, ReportBy: base.Add(3 * time.Hour)},
		}},
		Feedback: []delivery.Item{{
			Ref:      delivery.DeliveryRef{Sender: addr(local, 1), Message: msgID(7), Recipient: addr(2, 2)},
			Feedback: delivery.Feedback{Kind: delivery.KindDeliveryUnknown, State: delivery.StateDeliveryUnknown}, At: base,
		}},
	}

	// Target records.
	in := tgtIn(1)
	in.Rev = 1
	recs["in accepted"] = in
	leased := tgtIn(2)
	leased.Rev = 5
	lid, _ := delivery.ParseLeaseID("00112233445566778899aabbccddeeff")
	leased.Record.Sub = delivery.SubNotified
	leased.Record.Reported = delivery.KindSet(0).With(delivery.KindDeliveryBlocked)
	leased.Record.Blocked, leased.Record.BlockedCause = true, delivery.CauseRuntimeAuth
	leased.Record.Lease = delivery.Lease{
		Handouts: 2, IDs: []delivery.LeaseID{lid}, Active: true,
		IssuedAt: base.Add(time.Minute), ExpiresAt: base.Add(6 * time.Minute),
		Chunks: 2, HandedOut: []bool{true, false}, Acked: []bool{true, false},
	}
	leased.ReplyDecision = reply.Decision{Reason: reply.ReasonDeliveredWindow, Until: base.Add(time.Hour)}
	leased.Handout = store.HandoutFields{ContentType: "text/markdown", ReplyTo: msgID(1), Thread: "t", Seq: 12, SentAt: base}
	recs["in leased, notified and blocked"] = leased
	rejected := tgtIn(3)
	rejected.Rev = 1
	rejected.Body = nil
	rejected.Auth = delivery.Authorization{Kind: delivery.AuthorizationDeny, Reject: delivery.RejectUnauthorized}
	rejected.Record.State, rejected.Record.Reject = delivery.StateRejected, delivery.RejectUnauthorized
	recs["in rejected without a body"] = rejected
	dv := tgtIn(4)
	dv.Rev = 2
	dv.Key.Sender = ep(local) // the notice and the derived delivery have local senders
	dv.Dedup = delivery.NewDedupRecord(dv.Key.Dedup(), digestOf(4), dv.Record.ReportBy)
	dv.Derived = &store.DerivedInfo{From: ep(4), AuthRequest: []byte(`{"stage":"intake"}`)}
	recs["in derived from a session creation"] = dv
	notice := tgtIn(5)
	notice.Rev = 1
	notice.Key = store.TargetKey{SenderNode: node(local), Sender: ident.SystemEndpoint(), Message: msgID(5), Recipient: ep(1)}
	notice.Dedup = delivery.NewDedupRecord(notice.Key.Dedup(), digestOf(5), notice.Record.ReportBy)
	notice.Covers = []store.CoverRef{{Out: srcOut(4, false).Name(), Recipient: addr(2, 2), Kind: delivery.KindRejected}}
	recs["in notice that covers feedback"] = notice
	recs["in lost"] = store.InRecord{
		Key: tgtKey(6), Lost: true, DiscoveredAt: base.Add(time.Hour),
		Record: delivery.Record{Side: delivery.SideTarget, State: delivery.StateDeliveryUnknown, AcceptBefore: base, ReportBy: base.Add(3 * time.Hour)},
		Dedup:  delivery.NewDedupRecord(tgtKey(6).Dedup(), digestOf(6), base.Add(3*time.Hour)),
	}

	// Control records.
	c := sessionCtl(1, 5)
	recs["ctl creating"] = c
	created := sessionCtl(2, 6)
	created.Intent.State = store.IntentCreated
	created.Intent.NativeRef = "native-1"
	created.Intent.Process = &adapterproto.ProcessIdentity{PID: 4242, StartKey: "boot1:77"}
	created.Body = func() *store.BodyRef { b := store.BodyRefOf(bodyBytes(2)); return &b }()
	created.EndpointMaxQueue = 30 * time.Minute
	reply1, _ := created.Dedup.WithReply([]byte("the stored control reply"))
	created.Dedup = reply1
	created.Result = []byte(`{"created":true}`)
	recs["ctl created with a stored reply"] = created
	closeCtl := sessionCtl(3, 6)
	closeCtl.Kind = envelope.KindSessionClose
	closeCtl.Preset, closeCtl.SessionEndpoint, closeCtl.Intent = "", ident.EndpointID{}, nil
	closeCtl.ClosedEndpoint = ep(6)
	recs["ctl that closed a session"] = closeCtl
	recs["ctl lost"] = store.CtlRecord{
		Key: ctlKey(4), Lost: true, DiscoveredAt: base.Add(time.Hour),
		Record: delivery.Record{Side: delivery.SideTarget, State: delivery.StateDeliveryUnknown, Control: true, AcceptBefore: base, ReportBy: base.Add(3 * time.Hour)},
		Dedup:  delivery.NewDedupRecord(ctlKey(4).Dedup(), digestOf(4), base.Add(3*time.Hour)),
	}

	// The rest.
	recs["endpoint direct"] = directEndpoint(7, "chat")
	rt := directEndpoint(8, "claude")
	rt.Kind = adapterreg.KindRuntime
	rt.Declaration = nil
	recs["endpoint runtime without a declaration"] = rt
	op := session(9, store.EndpointOpen, strings.TrimSuffix(ctlKey(2).FileName(store.KindCtl), ".rec"))
	op.Session.NativeRef = "native-9"
	op.Session.Process = &adapterproto.ProcessIdentity{PID: 99, StartKey: "k", StartedAtMS: 1700000000000}
	recs["endpoint session open"] = op
	recs["endpoint session closed"] = session(10, store.EndpointClosed, strings.TrimSuffix(ctlKey(2).FileName(store.KindCtl), ".rec"))
	recs["attempts empty"] = store.AttemptsRecord{Endpoint: ep(3)}
	recs["attempts in flight and unblocked"] = store.AttemptsRecord{
		Endpoint: ep(4),
		Attempts: []handoff.Attempt{
			{At: base, Outcome: handoff.AttemptFailed, Kind: handoff.PromptKindFirst},
			{At: base.Add(time.Minute), Outcome: handoff.AttemptUnknown, Kind: handoff.PromptKindReprompt},
		},
		InFlight: &store.InFlightPrompt{Attempt: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6", Kind: handoff.PromptKindFirst, Covers: []store.TargetKey{tgtKey(1), tgtKey(2)}},
		Pending: store.Pending{Event: store.PendingBlocked, Cause: delivery.CauseNativeUnavailable, Items: []store.PendingItem{
			{Key: tgtKey(1), Rev: 1, AcceptedAt: base}, {Key: tgtKey(2), Rev: 4, AcceptedAt: base.Add(time.Minute)},
		}},
	}
	recs["attempts recovered"] = store.AttemptsRecord{
		Endpoint: ep(6),
		Pending:  store.Pending{Event: store.PendingUnblocked, Cause: delivery.CauseAdapterOffline},
	}
	recs["alert unacknowledged"] = store.AlertRecord{ID: store.AlertID{1, 2, 3}, Kind: "control_unknown", At: base, Detail: "a native process may remain"}
	recs["seq empty floors"] = store.SeqRecord{Sender: ep(2), Floors: map[ident.Address]uint64{}}
	recs["seq two floors"] = store.SeqRecord{Sender: ep(3), Floors: map[ident.Address]uint64{addr(2, 2): 7, addr(local, 4): 1}}
	recs["checkpoint session scope"] = store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{}`)}
	return recs
}

// The first line of a frame is the one place the kind and the key are written,
// and the file name is the way they are checked: flipping any single bit of
// the first line of a record file changes what DecodeRecord sees, and no flip
// of the first line is a body damage with a different key.
func TestFirstLineFlipsNeverGiveATrustedWrongKey(t *testing.T) {
	key := []byte("node1abc ep1def message-1 ep1ghi")
	file, err := recfile.EncodeFrame("in", key, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	name := recfile.FileName("in", key)
	header := bytes.IndexByte(file, '\n')
	for i := 0; i < header; i++ {
		for bit := 0; bit < 8; bit++ {
			flipped := append([]byte(nil), file...)
			flipped[i] ^= 1 << bit
			_, dmg := store.DecodeRecord(store.InDir, name, flipped)
			if dmg == nil {
				t.Fatalf("flipping bit %d of byte %d of the first line was accepted", bit, i)
			}
			if dmg.Class == store.BodyDamaged && (dmg.Kind != "in" || !bytes.Equal(dmg.Key, key)) {
				t.Fatalf("flip of byte %d bit %d: a body damage with the wrong kind or key %q %q", i, bit, dmg.Kind, dmg.Key)
			}
		}
	}
}
