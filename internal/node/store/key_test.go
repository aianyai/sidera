package store_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// The identifiers of the fixtures, written out: node(2), ep(1), ep(2), ep(3).
const (
	n2  = "node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd"
	e1  = "ep1zyg3zyg3zyg3zyg3zyg3zyg3zyj0kcgq"
	e2  = "ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc"
	e3  = "ep1zvf3xycnzvf3xycnzvf3xycnzvl5u4cd"
	p1  = "person1yysjzgfpyysjzgfpyysjzgfpyysjzgfpyysjzgfpyyssjz24yx"
	op  = "0123456789abcdef0123456789abcdef"
	aid = "00112233445566778899aabbccddeeff"
)

func TestKeyTextGoldenVectors(t *testing.T) {
	t.Run("target key", func(t *testing.T) {
		k := tgtKey(1)
		want := n2 + " " + e3 + " msg-0001 " + e2
		b, err := k.Bytes()
		if err != nil || string(b) != want {
			t.Fatalf("Bytes() = %q, %v, want %q", b, err, want)
		}
		if k.String() != want {
			t.Errorf("String() = %q, want %q", k.String(), want)
		}
		back, err := store.ParseTargetKey(want)
		if err != nil || back != k {
			t.Errorf("ParseTargetKey = %v, %v, want %v", back, err, k)
		}
		if d := k.Dedup(); d.SenderNode != node(2) || d.Message != msgID(1) || d.Recipient != ep(2) {
			t.Errorf("Dedup() = %+v", d)
		}
	})
	t.Run("out key", func(t *testing.T) {
		k := outKey(1, false)
		want := e1 + " msg-0001 " + n2 + "/" + e2
		b, err := k.Bytes()
		if err != nil || string(b) != want {
			t.Fatalf("Bytes() = %q, %v, want %q", b, err, want)
		}
		if k.Kind() != store.KindOut {
			t.Errorf("Kind() = %q", k.Kind())
		}
		k.Control = true
		b2, _ := k.Bytes()
		if string(b2) != want {
			t.Errorf("a control signal has the same key text, got %q", b2)
		}
		if k.Kind() != store.KindOutCtl {
			t.Errorf("Kind() of a control signal = %q", k.Kind())
		}
		if k.FileName() == outKey(1, false).FileName() {
			t.Error("a message and a control signal with one key text must have different file names")
		}
	})
	t.Run("endpoint, checkpoint, admin and alert keys", func(t *testing.T) {
		if got := string(store.EndpointKeyBytes(ep(1))); got != e1 {
			t.Errorf("EndpointKeyBytes = %q, want %q", got, e1)
		}
		cp, err := store.CheckpointKeyBytes(store.CheckpointKey{Scope: "attempt", ID: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6"})
		if err != nil || string(cp) != "attempt 0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6" {
			t.Errorf("CheckpointKeyBytes = %q, %v", cp, err)
		}
		var opid admin.OperationID
		copy(opid[:], []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef})
		if got := string(store.AdminKeyBytes(admin.RequestKey{ID: opid})); got != "local "+op {
			t.Errorf("AdminKeyBytes of the local owner = %q, want %q", got, "local "+op)
		}
		person, _ := ident.PersonIDFromBytes(bytes.Repeat([]byte{0x21}, ident.PersonIDLen))
		if got := string(store.AdminKeyBytes(admin.RequestKey{Person: person, ID: opid})); got != p1+" "+op {
			t.Errorf("AdminKeyBytes of a person = %q, want %q", got, p1+" "+op)
		}
		var id store.AlertID
		copy(id[:], []byte{0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
		if id.String() != aid || string(store.AlertKeyBytes(id)) != aid {
			t.Errorf("AlertID text = %q, want %q", id.String(), aid)
		}
	})
}

func TestParseKeyRoundTripsEveryKind(t *testing.T) {
	var opid admin.OperationID
	opid[0], opid[15] = 1, 2
	var alert store.AlertID
	alert[3] = 9
	person, _ := ident.PersonIDFromBytes(bytes.Repeat([]byte{0x21}, ident.PersonIDLen))
	cp := store.CheckpointKey{Scope: "session", ID: e2}
	cpb, _ := store.CheckpointKeyBytes(cp)
	ob, _ := outKey(7, true).Bytes()
	ctlb, _ := ctlKey(8).Bytes()
	tb, _ := tgtKey(9).Bytes()
	for _, c := range []struct {
		kind store.Kind
		key  []byte
		want any
	}{
		{store.KindOut, ob, outKey(7, true)},
		{store.KindIn, tb, tgtKey(9)},
		{store.KindCtl, ctlb, ctlKey(8)},
		{store.KindEndpoint, store.EndpointKeyBytes(ep(5)), ep(5)},
		{store.KindAttempts, store.EndpointKeyBytes(ep(5)), ep(5)},
		{store.KindSeq, store.EndpointKeyBytes(ep(5)), ep(5)},
		{store.KindCheckpoint, cpb, cp},
		{store.KindAdmin, store.AdminKeyBytes(admin.RequestKey{ID: opid}), admin.RequestKey{ID: opid}},
		{store.KindAdmin, store.AdminKeyBytes(admin.RequestKey{Person: person, ID: opid}), admin.RequestKey{Person: person, ID: opid}},
		{store.KindAlert, store.AlertKeyBytes(alert), alert},
	} {
		got, err := store.ParseKey(c.kind, c.key)
		if err != nil {
			t.Errorf("ParseKey(%s, %q): %v", c.kind, c.key, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseKey(%s, %q) = %#v, want %#v", c.kind, c.key, got, c.want)
		}
	}
	// The control kind parses to a key with Control set.
	got, err := store.ParseKey(store.KindOutCtl, ob)
	if err != nil {
		t.Fatal(err)
	}
	if k := got.(store.OutKey); !k.Control || k.Message != msgID(7) {
		t.Errorf("ParseKey(out-ctl) = %+v, want a control key", k)
	}
}

func TestParseKeyRefusals(t *testing.T) {
	good := string(mustTarget(t, tgtKey(1)))
	fields := strings.Split(good, " ")
	join := func(f ...string) string { return strings.Join(f, " ") }
	long := strings.Repeat("m", envelope.MaxIDLen+1)
	var recipients []string
	for i := 0; i < envelope.MaxRecipients+1; i++ {
		recipients = append(recipients, n2+"/"+e2)
	}
	for _, c := range []struct {
		name string
		kind store.Kind
		key  string
	}{
		{"in key with three fields", store.KindIn, join(fields[0], fields[1], fields[2])},
		{"in key with five fields", store.KindIn, join(good, "x")},
		{"in key with a node that is not a node", store.KindIn, join(e3, fields[1], fields[2], fields[3])},
		{"in key with a damaged endpoint checksum", store.KindIn, join(fields[0], fields[1][:len(fields[1])-1]+"q", fields[2], fields[3])},
		{"in key with an endpoint where the sender node goes", store.KindIn, join(e1, fields[1], fields[2], fields[3])},
		{"in key with an empty message id", store.KindIn, join(fields[0], fields[1], "", fields[3])},
		{"in key with a message id over the limit", store.KindIn, join(fields[0], fields[1], long, fields[3])},
		{"in key with a message id of control characters", store.KindIn, join(fields[0], fields[1], "a\x00b", fields[3])},
		{"in key with a trailing space", store.KindIn, good + " "},
		{"out key with no recipient", store.KindOut, join(e1, "m")},
		{"out key with a recipient that is not an address", store.KindOut, join(e1, "m", e2)},
		{"out key with a node-less recipient", store.KindOut, join(e1, "m", "/"+e2)},
		{"out key with more recipients than an envelope has", store.KindOut, join(e1, "m", strings.Join(recipients, " "))},
		{"endpoint key that is an address", store.KindEndpoint, n2 + "/" + e2},
		{"endpoint key with two fields", store.KindEndpoint, e1 + " x"},
		{"checkpoint key with an unknown scope", store.KindCheckpoint, "global x"},
		{"checkpoint key without an id", store.KindCheckpoint, "attempt"},
		{"admin key with an operation in capitals", store.KindAdmin, "local 0123456789ABCDEF0123456789abcdef"},
		{"admin key with a short operation", store.KindAdmin, "local 0123"},
		{"admin key with a person that is not one", store.KindAdmin, "someone " + op},
		{"admin key of a zero operation", store.KindAdmin, "local 00000000000000000000000000000000"},
		{"alert key in capitals", store.KindAlert, strings.ToUpper(aid)},
		{"alert key of 31 digits", store.KindAlert, aid[1:]},
		{"alert key of zeros", store.KindAlert, strings.Repeat("0", 32)},
		{"a kind that is not a kind of state", store.Kind("clock"), "clock"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got, err := store.ParseKey(c.kind, []byte(c.key)); err == nil {
				t.Errorf("ParseKey(%s, %q) = %#v, want an error", c.kind, c.key, got)
			}
		})
	}
}

func mustTarget(t *testing.T, k store.TargetKey) []byte {
	t.Helper()
	b, err := k.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A key that cannot be written is refused when the record is encoded, not when
// it is read: a message id with a space would be read as two fields.
func TestKeysThatCannotBeWrittenAreRefused(t *testing.T) {
	bad := tgtKey(1)
	bad.Message = "has a space"
	if _, err := bad.Bytes(); err == nil {
		t.Error("a message id with spaces was accepted by TargetKey.Bytes")
	}
	bad.Message = ""
	if _, err := bad.Bytes(); err == nil {
		t.Error("an empty message id was accepted")
	}
	o := outKey(1, false)
	o.Recipients = nil
	if _, err := o.Bytes(); err == nil {
		t.Error("an out key without recipients was accepted")
	}
	if _, err := store.CheckpointKeyBytes(store.CheckpointKey{Scope: "attempt", ID: "a b"}); err == nil {
		t.Error("a checkpoint id with a space was accepted")
	}
}

// AlertID and the key texts of the fixtures are the same strings the
// contract packages produce: the store does not make text up.
func TestKeyFieldsUseTheTextOfTheirOwnPackages(t *testing.T) {
	k := tgtKey(3)
	b := string(mustTarget(t, k))
	for _, part := range []string{k.SenderNode.String(), k.Sender.String(), string(k.Message), k.Recipient.String()} {
		if !strings.Contains(b, part) {
			t.Errorf("key text %q lacks %q", b, part)
		}
	}
	o, _ := outKey(3, false).Bytes()
	if !strings.Contains(string(o), outKey(3, false).Recipients[0].String()) {
		t.Errorf("out key text %q lacks the address text", o)
	}
}

// reencodeSimpleKey writes back a key that is not one of the structured ones.
func reencodeSimpleKey(t *testing.T, kind store.Kind, k any) []byte {
	t.Helper()
	switch v := k.(type) {
	case ident.EndpointID:
		return store.EndpointKeyBytes(v)
	case admin.RequestKey:
		return store.AdminKeyBytes(v)
	case store.AlertID:
		return store.AlertKeyBytes(v)
	}
	t.Fatalf("kind %s parsed to the unexpected type %T", kind, k)
	return nil
}
