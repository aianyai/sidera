package envelope

import (
	"bytes"
	"errors"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func baseMessage(t testing.TB) Envelope {
	t.Helper()
	e, err := Decode(goldenEnvelopeMessage, testLimits)
	requireNoErr(t, err)
	return e
}

func baseCreate(t testing.TB) Envelope {
	t.Helper()
	e, err := Decode(goldenEnvelopeSessionCreate, testLimits)
	requireNoErr(t, err)
	return e
}

func wireOf(t testing.TB, e Envelope) envelopeWire {
	t.Helper()
	w, err := e.toWire()
	requireNoErr(t, err)
	return w
}

// decodeWire puts a wire struct through the canonical encoder and the decoder,
// as a peer's bytes would arrive.
func decodeWire(t testing.TB, w envelopeWire, lim Limits) error {
	t.Helper()
	b, err := record.MarshalCanonical(w)
	requireNoErr(t, err)
	_, err = Decode(b, lim)
	return err
}

func TestEnvelopeDecodeRules(t *testing.T) {
	validRecord := func(typ string) []byte {
		b, err := record.Record{Type: typ, Payload: []byte{0xa0}, Signatures: mustDecodeRecord(t, goldenReportDelivered).Signatures}.Encode()
		requireNoErr(t, err)
		return b
	}
	addrWire := func(n ident.NodeID, b byte) addressWire {
		return addressWire{Node: n.Bytes(), Endpoint: ep(b).Bytes()}
	}
	nodeA, nodeB := nodeOf(t, keyIdentityA), nodeOf(t, keyIdentityB)
	long := strings.Repeat("x", MaxIDLen+1)

	message := func(t testing.TB) envelopeWire { return wireOf(t, baseMessage(t)) }
	create := func(t testing.TB) envelopeWire { return wireOf(t, baseCreate(t)) }
	closeSignal := func(t testing.TB) envelopeWire {
		w := create(t)
		w.Kind, w.ContentType, w.Body, w.Params = wireKindSessionClose, "", nil, nil
		return w
	}
	withEvidence := func(t testing.TB, evidence ...[]byte) envelopeWire {
		w := message(t)
		w.ReplyTo, w.Evidence = "ctl-0001", evidence
		return w
	}

	cases := []struct {
		rule  string
		build func(testing.TB) envelopeWire
		mut   func(*envelopeWire)
		want  error
	}{
		{"the type string is the envelope's", message, func(w *envelopeWire) { w.Type = "sidera/envelope/2" }, ErrType},
		{"an id is not empty", message, func(w *envelopeWire) { w.ID = "" }, ErrID},
		{"an id has no space", message, func(w *envelopeWire) { w.ID = "a b" }, ErrID},
		{"an id has no control byte", message, func(w *envelopeWire) { w.ID = "a\x07b" }, ErrID},
		{"an id is ASCII", message, func(w *envelopeWire) { w.ID = "mé" }, ErrID},
		{"an id is at most 128 bytes", message, func(w *envelopeWire) { w.ID = long }, ErrID},
		{"reply_to is an id", message, func(w *envelopeWire) { w.ReplyTo = "a b" }, ErrID},
		{"thread is an id", message, func(w *envelopeWire) { w.Thread = long }, ErrID},
		{"kind zero is invalid", message, func(w *envelopeWire) { w.Kind = 0 }, ErrKind},
		{"kind six is invalid", message, func(w *envelopeWire) { w.Kind = 6 }, ErrKind},
		{"at least one recipient", message, func(w *envelopeWire) { w.To = nil }, ErrRecipients},
		{"at most MaxRecipients recipients", message, func(w *envelopeWire) {
			w.MultiRecipient = true
			for i := 0; i < testLimits.MaxRecipients; i++ {
				w.To = append(w.To, addrWire(nodeA, byte(i+10)))
			}
		}, ErrRecipients},
		{"a recipient node is not zero", message, func(w *envelopeWire) { w.To[0].Node = make([]byte, 32) }, ErrRecipients},
		{"a recipient endpoint has 16 bytes", message, func(w *envelopeWire) { w.To[0].Endpoint = []byte{1} }, ErrRecipients},
		{"the system endpoint is not a recipient", message, func(w *envelopeWire) {
			w.To[0].Endpoint = ident.SystemEndpoint().Bytes()
			w.Seq[0].Endpoint = ident.SystemEndpoint().Bytes()
		}, ErrSystemRecipient},
		{"the system endpoint is not a recipient among several", message, func(w *envelopeWire) {
			w.MultiRecipient = true
			w.To = append(w.To, addrWire(nodeA, 9), addressWire{Node: nodeB.Bytes(), Endpoint: ident.SystemEndpoint().Bytes()})
			w.Seq = append(w.Seq, seqWire{Endpoint: ep(9).Bytes(), Seq: 1})
		}, ErrSystemRecipient},
		{"the system endpoint is not the recipient of a control signal", create, func(w *envelopeWire) {
			w.To[0].Endpoint = ident.SystemEndpoint().Bytes()
			w.Seq[0].Endpoint = ident.SystemEndpoint().Bytes()
		}, ErrSystemRecipient},
		{"an address is listed once", message, func(w *envelopeWire) {
			w.MultiRecipient = true
			w.To = append(w.To, w.To[0])
		}, ErrRecipients},
		{"an endpoint is listed once even on two nodes", message, func(w *envelopeWire) {
			w.MultiRecipient = true
			w.To = append(w.To, addrWire(nodeB, 1))
		}, ErrRecipients},
		{"several recipients need the multi-recipient flag", message, func(w *envelopeWire) {
			w.To = append(w.To, addrWire(nodeA, 9))
			w.Seq = append(w.Seq, seqWire{Endpoint: ep(9).Bytes(), Seq: 1})
		}, ErrRecipients},
		{"a control signal has one recipient", create, func(w *envelopeWire) {
			w.MultiRecipient = true
			w.To = append(w.To, addrWire(nodeA, 9))
			w.Seq = append(w.Seq, seqWire{Endpoint: ep(9).Bytes(), Seq: 1})
		}, ErrRecipients},
		{"a control signal is not multi-recipient", create, func(w *envelopeWire) { w.MultiRecipient = true }, ErrRecipients},
		{"a disclosed list of one is not multi-recipient", message, func(w *envelopeWire) { w.MultiRecipient, w.DiscloseRecipients = true, true }, ErrRecipients},
		{"a control signal answers no message", create, func(w *envelopeWire) { w.ReplyTo = "msg-0001" }, ErrReplyTo},
		{"session.close answers no message", closeSignal, func(w *envelopeWire) { w.ReplyTo = "msg-0001" }, ErrReplyTo},
		{"a control signal carries no evidence", create, func(w *envelopeWire) {
			w.Evidence = [][]byte{mustEncodeRecord(t, mustDecodeRecord(t, goldenReportDelivered))}
		}, ErrEvidence},
		{"a control signal carries no evidence even with a reply_to", create, func(w *envelopeWire) {
			w.ReplyTo = "msg-0001"
			w.Evidence = [][]byte{mustEncodeRecord(t, mustDecodeRecord(t, goldenReportDelivered))}
		}, ErrReplyTo},
		{"there is a sequence number", message, func(w *envelopeWire) { w.Seq = nil }, ErrSeq},
		{"a sequence number belongs to a recipient", message, func(w *envelopeWire) { w.Seq[0].Endpoint = ep(9).Bytes() }, ErrSeq},
		{"a sequence number is at least 1", message, func(w *envelopeWire) { w.Seq[0].Seq = 0 }, ErrSeq},
		{"there are no more sequence numbers than recipients", message, func(w *envelopeWire) {
			w.Seq = append(w.Seq, seqWire{Endpoint: ep(2).Bytes(), Seq: 1})
		}, ErrSeq},
		{"sequence numbers ascend by endpoint", message, func(w *envelopeWire) {
			w.MultiRecipient = true
			w.To = append(w.To, addrWire(nodeA, 9))
			w.Seq = []seqWire{{Endpoint: ep(9).Bytes(), Seq: 1}, {Endpoint: ep(1).Bytes(), Seq: 1}}
		}, ErrSeq},
		{"a sequence entry is not repeated", message, func(w *envelopeWire) {
			w.MultiRecipient = true
			w.To = append(w.To, addrWire(nodeA, 9))
			w.Seq = []seqWire{{Endpoint: ep(1).Bytes(), Seq: 1}, {Endpoint: ep(1).Bytes(), Seq: 2}}
		}, ErrSeq},
		{"sent_at is required", message, func(w *envelopeWire) { w.SentAt = 0 }, record.ErrTime},
		{"sent_at is positive", message, func(w *envelopeWire) { w.SentAt = -5 }, record.ErrTime},
		{"accept_before is required", message, func(w *envelopeWire) { w.AcceptBefore = 0 }, record.ErrTime},
		{"report_by is required", message, func(w *envelopeWire) { w.ReportBy = 0 }, record.ErrTime},
		{"report_by follows from accept_before", message, func(w *envelopeWire) { w.ReportBy++ }, ErrReportBy},
		{"report_by is not accept_before", message, func(w *envelopeWire) { w.ReportBy = w.AcceptBefore }, ErrReportBy},
		{"a content type comes with a body", message, func(w *envelopeWire) { w.Body = nil }, ErrBody},
		{"a content type comes with a body, on a session.create too", create, func(w *envelopeWire) { w.Body = nil }, ErrBody},
		{"a body comes with a content type, on a session.create too", create, func(w *envelopeWire) { w.ContentType = "" }, ErrBody},
		{"a body comes with a content type", message, func(w *envelopeWire) { w.ContentType = "" }, ErrBody},
		{"a message has a body", message, func(w *envelopeWire) { w.Body, w.ContentType = nil, "" }, ErrBody},
		{"a body is at most MaxBody", message, func(w *envelopeWire) { w.Body = bytes.Repeat([]byte{'a'}, testLimits.MaxBody+1) }, ErrBodyTooLarge},
		{"a content type is printable ASCII", message, func(w *envelopeWire) { w.ContentType = "text/\x01" }, ErrBody},
		{"a content type is at most 128 bytes", message, func(w *envelopeWire) { w.ContentType = strings.Repeat("a", MaxContentTypeLen+1) }, ErrBody},
		{"a message has no params", message, func(w *envelopeWire) { w.Params = &paramsWire{Preset: "default"} }, ErrParams},
		{"session.create needs params", create, func(w *envelopeWire) { w.Params = nil }, ErrParams},
		{"a preset is not empty", create, func(w *envelopeWire) { w.Params.Preset = "" }, ErrParams},
		{"a preset is lowercase", create, func(w *envelopeWire) { w.Params.Preset = "Default" }, ErrParams},
		{"a preset is at most 64 characters", create, func(w *envelopeWire) { w.Params.Preset = strings.Repeat("a", 65) }, ErrParams},
		{"session.close has no body", closeSignal, func(w *envelopeWire) { w.ContentType, w.Body = "text/plain", []byte("x") }, ErrBody},
		{"session.close has no params", closeSignal, func(w *envelopeWire) { w.Params = &paramsWire{Preset: "default"} }, ErrParams},
		{"session.interrupt has no body", closeSignal, func(w *envelopeWire) {
			w.Kind, w.ContentType, w.Body = wireKindSessionInterrupt, "text/plain", []byte("x")
		}, ErrBody},
		{"endpoint.status has no body", closeSignal, func(w *envelopeWire) {
			w.Kind, w.ContentType, w.Body = wireKindEndpointStatus, "text/plain", []byte("x")
		}, ErrBody},
		{"endpoint.status has no params", closeSignal, func(w *envelopeWire) {
			w.Kind, w.Params = wireKindEndpointStatus, &paramsWire{Preset: "default"}
		}, ErrParams},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			w := c.build(t)
			c.mut(&w)
			requireErr(t, decodeWire(t, w, testLimits), c.want)
		})
	}

	evidenceCases := []struct {
		rule string
		w    func(testing.TB) envelopeWire
	}{
		{"at most two evidence records", func(t testing.TB) envelopeWire {
			return withEvidence(t, validRecord(ControlReplyType), validRecord(StatusReportType), validRecord(StatusReportType))
		}},
		{"evidence is a control reply or a status report", func(t testing.TB) envelopeWire {
			return withEvidence(t, validRecord("sidera/node-declaration/1"))
		}},
		{"a control reply comes before a status report", func(t testing.TB) envelopeWire {
			return withEvidence(t, validRecord(StatusReportType), validRecord(ControlReplyType))
		}},
		{"at most one control reply", func(t testing.TB) envelopeWire {
			return withEvidence(t, validRecord(ControlReplyType), validRecord(ControlReplyType))
		}},
		{"at most one status report", func(t testing.TB) envelopeWire {
			return withEvidence(t, validRecord(StatusReportType), validRecord(StatusReportType))
		}},
		{"evidence is a record", func(t testing.TB) envelopeWire { return withEvidence(t, []byte{0x01}) }},
		{"evidence refers to a reply_to", func(t testing.TB) envelopeWire {
			w := withEvidence(t, validRecord(StatusReportType))
			w.ReplyTo = ""
			return w
		}},
	}
	for _, c := range evidenceCases {
		t.Run(c.rule, func(t *testing.T) { requireErr(t, decodeWire(t, c.w(t), testLimits), ErrEvidence) })
	}

	t.Run("evidence of a report alone, of a reply alone and of both is accepted", func(t *testing.T) {
		for _, ev := range [][][]byte{
			{validRecord(StatusReportType)}, {validRecord(ControlReplyType)}, {validRecord(ControlReplyType), validRecord(StatusReportType)},
		} {
			requireNoErr(t, decodeWire(t, withEvidence(t, ev...), testLimits))
		}
	})
}

func TestEnvelopeValidVariants(t *testing.T) {
	t.Run("session.create without an initial message", func(t *testing.T) {
		e := baseCreate(t)
		e.ContentType, e.Body = "", nil
		b, err := e.Encode(testLimits)
		requireNoErr(t, err)
		got, err := Decode(b, testLimits)
		requireNoErr(t, err)
		if len(got.Body) != 0 || got.ContentType != "" {
			t.Errorf("body survived: %+v", got)
		}
	})
	t.Run("a trimmed envelope with one recipient may carry the multi-recipient flag", func(t *testing.T) {
		e := baseMessage(t)
		e.MultiRecipient = true
		_, err := e.Encode(testLimits)
		requireNoErr(t, err)
	})
	t.Run("a disclosed list of several recipients carries the flag", func(t *testing.T) {
		e, err := Decode(goldenEnvelopeReply, testLimits)
		requireNoErr(t, err)
		if !e.MultiRecipient || !e.DiscloseRecipients || len(e.To) != 2 {
			t.Fatalf("setup: %+v", e)
		}
	})
	t.Run("a body of exactly MaxBody bytes", func(t *testing.T) {
		e := baseMessage(t)
		e.Body = bytes.Repeat([]byte{'a'}, testLimits.MaxBody)
		_, err := e.Encode(testLimits)
		requireNoErr(t, err)
	})
	t.Run("the accept_before cap is not the envelope's to judge", func(t *testing.T) {
		e := baseMessage(t)
		e.AcceptBefore = t0.Add(1000 * time.Hour)
		e.ReportBy = delivery.ReportBy(e.AcceptBefore)
		_, err := e.Encode(testLimits)
		requireNoErr(t, err)
	})
	t.Run("session.interrupt and endpoint.status without body or params", func(t *testing.T) {
		for _, k := range []Kind{KindSessionClose, KindSessionInterrupt, KindEndpointStatus} {
			e := baseCreate(t)
			e.Kind, e.ContentType, e.Body, e.Preset = k, "", nil, ""
			_, err := e.Encode(testLimits)
			requireNoErr(t, err)
		}
	})
}

func TestEnvelopeLimitsAreParameters(t *testing.T) {
	for _, lim := range []Limits{{}, {MaxBody: 10}, {MaxRecipients: 10}, {MaxBody: -1, MaxRecipients: 1}} {
		if _, err := Decode(goldenEnvelopeMessage, lim); !isErr(err, ErrLimits) {
			t.Errorf("Decode with %+v: %v, want ErrLimits", lim, err)
		}
		if _, err := baseMessage(t).Encode(lim); !isErr(err, ErrLimits) {
			t.Errorf("Encode with %+v: %v, want ErrLimits", lim, err)
		}
	}
	small := Limits{MaxBody: 4, MaxRecipients: 64}
	if _, err := Decode(goldenEnvelopeMessage, small); !isErr(err, ErrBody) {
		t.Errorf("body over a small limit: %v", err)
	}
	few := Limits{MaxBody: 1 << 20, MaxRecipients: 1}
	if _, err := Decode(goldenEnvelopeReply, few); !isErr(err, ErrRecipients) {
		t.Errorf("two recipients over a limit of one: %v", err)
	}
}

func isErr(err, target error) bool { return errors.Is(err, target) }

func TestEnvelopeStrictBytes(t *testing.T) {
	t.Run("input over the limit is refused first", func(t *testing.T) {
		huge := bytes.Repeat([]byte{0}, Limits{MaxBody: 1, MaxRecipients: 1}.inputLimit()+1)
		_, err := Decode(huge, Limits{MaxBody: 1, MaxRecipients: 1})
		requireErr(t, err, record.ErrOversize)
	})
	t.Run("trailing bytes", func(t *testing.T) {
		_, err := Decode(append(bytes.Clone(goldenEnvelopeMessage), 0), testLimits)
		requireErr(t, err, record.ErrMalformed)
	})
	t.Run("a non-shortest integer", func(t *testing.T) {
		b := append([]byte{goldenEnvelopeMessage[0], 0x18, 0x01}, goldenEnvelopeMessage[2:]...)
		_, err := Decode(b, testLimits)
		requireErr(t, err, record.ErrNotCanonical)
	})
	t.Run("an unknown field", func(t *testing.T) {
		var m map[uint64]cbor.RawMessage
		requireNoErr(t, cbor.Unmarshal(goldenEnvelopeMessage, &m))
		m[17] = cbor.RawMessage{0x01}
		em, _ := cbor.CoreDetEncOptions().EncMode()
		b, err := em.Marshal(m)
		requireNoErr(t, err)
		_, err = Decode(b, testLimits)
		requireErr(t, err, record.ErrMalformed)
	})
	t.Run("a duplicate key", func(t *testing.T) {
		// the golden map has 15 pairs; claim 16 and repeat the type key
		b := append([]byte{0xb0}, goldenEnvelopeMessage[1:]...)
		b = append(b, 0x01, 0x71)
		b = append(b, []byte(Type)...)
		_, err := Decode(b, testLimits)
		requireErr(t, err, record.ErrMalformed)
	})
	t.Run("an indefinite-length map", func(t *testing.T) {
		b := append([]byte{0xbf}, goldenEnvelopeMessage[1:]...)
		b = append(b, 0xff)
		_, err := Decode(b, testLimits)
		requireErr(t, err, record.ErrMalformed)
	})
	t.Run("an empty list written where the key should be left out", func(t *testing.T) {
		w := wireOf(t, baseMessage(t))
		em, _ := cbor.CoreDetEncOptions().EncMode()
		var m map[uint64]cbor.RawMessage
		b, _ := record.MarshalCanonical(w)
		requireNoErr(t, cbor.Unmarshal(b, &m))
		m[9] = cbor.RawMessage{0x80}
		b, err := em.Marshal(m)
		requireNoErr(t, err)
		_, err = Decode(b, testLimits)
		requireErr(t, err, record.ErrNotCanonical)
	})
	t.Run("a text id that is not UTF-8", func(t *testing.T) {
		b := bytes.Replace(goldenEnvelopeMessage, []byte("msg-0001"), []byte("msg-\xff\xff01"), 1)
		_, err := Decode(b, testLimits)
		if err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestEncodeRefusesWhatDecodeRefuses(t *testing.T) {
	cases := map[string]func(*Envelope){
		"report_by not derived":                      func(e *Envelope) { e.ReportBy = e.AcceptBefore },
		"message without body":                       func(e *Envelope) { e.Body, e.ContentType = nil, "" },
		"zero sent time":                             func(e *Envelope) { e.SentAt = time.Time{} },
		"invalid kind":                               func(e *Envelope) { e.Kind = KindInvalid },
		"preset on a message":                        func(e *Envelope) { e.Preset = "default" },
		"no recipients":                              func(e *Envelope) { e.To = nil },
		"sequence for a stranger":                    func(e *Envelope) { e.Seq = map[ident.EndpointID]uint64{ep(9): 1} },
		"multi flag on a disclosed single recipient": func(e *Envelope) { e.MultiRecipient, e.DiscloseRecipients = true, true },
		"reply_to on a control signal": func(e *Envelope) {
			e.Kind, e.ContentType, e.Body, e.Preset, e.ReplyTo = KindEndpointStatus, "", nil, "", "other"
		},
		"evidence on a control signal": func(e *Envelope) {
			e.Kind, e.ContentType, e.Body, e.Preset, e.ReplyTo = KindEndpointStatus, "", nil, "", ""
			e.Evidence = []record.Record{mustDecodeRecord(t, goldenReportDelivered)}
		},
		"evidence of the wrong type": func(e *Envelope) {
			e.ReplyTo = "x"
			e.Evidence = []record.Record{{Type: "sidera/node-declaration/1", Payload: []byte{0xa0}, Signatures: mustDecodeRecord(t, goldenReportDelivered).Signatures}}
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			e := baseMessage(t)
			mut(&e)
			if _, err := e.Encode(testLimits); err == nil {
				t.Fatal("encoded")
			}
		})
	}
}

func TestKindMappings(t *testing.T) {
	cases := []struct {
		kind    Kind
		name    string
		wire    uint64
		action  authz.Action
		control bool
		signal  adapterproto.ControlSignal
	}{
		{KindMessage, "message", 1, authz.ActionMessageSend, false, ""},
		{KindSessionCreate, "control.session.create", 2, authz.ActionSessionCreate, true, adapterproto.SignalSessionCreate},
		{KindSessionClose, "control.session.close", 3, authz.ActionSessionClose, true, adapterproto.SignalSessionClose},
		{KindSessionInterrupt, "control.session.interrupt", 4, authz.ActionSessionInterrupt, true, adapterproto.SignalSessionInterrupt},
		{KindEndpointStatus, "control.endpoint.status", 5, authz.ActionEndpointStatus, true, adapterproto.SignalEndpointStatus},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.kind.String() != c.name || !c.kind.Valid() || c.kind.IsControl() != c.control {
				t.Errorf("name %q valid %v control %v", c.kind, c.kind.Valid(), c.kind.IsControl())
			}
			if w, err := c.kind.toWire(); err != nil || w != c.wire {
				t.Errorf("wire = %d, %v, want %d", w, err, c.wire)
			}
			if k, err := kindFromWire(c.wire); err != nil || k != c.kind {
				t.Errorf("from wire = %v, %v", k, err)
			}
			if a, ok := c.kind.Action(); !ok || a != c.action {
				t.Errorf("action = %v, %v, want %v", a, ok, c.action)
			}
			sig, isSignal := c.kind.Signal()
			if isSignal != c.control || sig != c.signal {
				t.Errorf("signal = %q, %v", sig, isSignal)
			}
			if c.control {
				if k, ok := KindOfSignal(c.signal); !ok || k != c.kind {
					t.Errorf("KindOfSignal(%q) = %v, %v", c.signal, k, ok)
				}
			}
		})
	}
	t.Run("the invalid kind has nothing", func(t *testing.T) {
		if _, ok := KindInvalid.Action(); ok {
			t.Error("action for the invalid kind")
		}
		if _, ok := KindInvalid.Signal(); ok {
			t.Error("signal for the invalid kind")
		}
		if _, ok := Kind(99).Action(); ok {
			t.Error("action for kind 99")
		}
		if _, err := Kind(99).toWire(); err == nil {
			t.Error("wire for kind 99")
		}
		if _, ok := KindOfSignal("session.nuke"); ok {
			t.Error("kind for an unknown signal")
		}
	})
	t.Run("no two kinds share an action", func(t *testing.T) {
		seen := map[authz.Action]Kind{}
		for _, c := range cases {
			if prev, dup := seen[c.action]; dup {
				t.Errorf("%v and %v share %v", prev, c.kind, c.action)
			}
			seen[c.action] = c.kind
		}
	})
}

func TestValidateFor(t *testing.T) {
	nodeA, nodeB := nodeOf(t, keyIdentityA), nodeOf(t, keyIdentityB)
	full := Envelope{
		ID: "m", To: []ident.Address{addr(nodeA, 1), addr(nodeB, 2), addr(nodeA, 3)}, MultiRecipient: true, Kind: KindMessage,
		Seq:    map[ident.EndpointID]uint64{ep(1): 1, ep(2): 1, ep(3): 5},
		SentAt: t0, AcceptBefore: t0.Add(time.Minute), ReportBy: delivery.ReportBy(t0.Add(time.Minute)),
		ContentType: "text/plain", Body: []byte("x"),
	}
	cases := []struct {
		rule string
		e    func() Envelope
		node ident.NodeID
		want error
	}{
		{"an envelope trimmed to the node is fine", func() Envelope { return full.TrimmedFor(nodeA) }, nodeA, nil},
		{"a disclosed full list is fine", func() Envelope { e := full; e.DiscloseRecipients = true; return e.TrimmedFor(nodeA) }, nodeA, nil},
		{"a recipient must be on the node", func() Envelope { return full.TrimmedFor(nodeB) }, nodeA, ErrRecipients},
		{"others' recipients need disclosure", func() Envelope { return full }, nodeA, ErrRecipients},
		{"the sequence numbers are those of the node's recipients", func() Envelope {
			e := full.TrimmedFor(nodeA)
			delete(e.Seq, ep(3))
			return e
		}, nodeA, ErrSeq},
		{"no sequence number of another node's recipient", func() Envelope {
			e := full.TrimmedFor(nodeA)
			e.Seq[ep(2)] = 1
			return e
		}, nodeA, ErrSeq},
		{"the local node is set", func() Envelope { return full.TrimmedFor(nodeA) }, ident.NodeID{}, ErrRecipients},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			err := c.e().ValidateFor(c.node)
			if c.want == nil {
				requireNoErr(t, err)
				return
			}
			requireErr(t, err, c.want)
		})
	}
}

// TestTrimmedForRandom checks the sender's trimming against the receiver's
// validation over random recipient sets, and counts the paths taken.
func TestTrimmedForRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	nodes := []ident.NodeID{nodeOf(t, keyIdentityA), nodeOf(t, keyIdentityB), nodeOf(t, keyThird)}
	var disclosed, undisclosed, multiNodes, singleNode int
	for i := 0; i < 400; i++ {
		n := 1 + rng.Intn(6)
		e := Envelope{
			ID: "m", Kind: KindMessage, MultiRecipient: n > 1, DiscloseRecipients: rng.Intn(2) == 0,
			Seq:    map[ident.EndpointID]uint64{},
			SentAt: t0, AcceptBefore: t0.Add(time.Minute), ReportBy: delivery.ReportBy(t0.Add(time.Minute)),
			ContentType: "text/plain", Body: []byte("x"),
		}
		onNodes := map[ident.NodeID]bool{}
		for j := 0; j < n; j++ {
			node := nodes[rng.Intn(len(nodes))]
			onNodes[node] = true
			a := addr(node, byte(10+j))
			e.To = append(e.To, a)
			e.Seq[a.Endpoint] = uint64(1 + rng.Intn(50))
		}
		if e.DiscloseRecipients {
			disclosed++
		} else {
			undisclosed++
		}
		if len(onNodes) > 1 {
			multiNodes++
		} else {
			singleNode++
		}
		if _, err := e.Encode(testLimits); err != nil {
			t.Fatalf("full envelope: %v", err)
		}
		total := 0
		for node := range onNodes {
			tr := e.TrimmedFor(node)
			if err := tr.ValidateFor(node); err != nil {
				t.Fatalf("trimmed for %v: %v", node, err)
			}
			b, err := tr.Encode(testLimits)
			if err != nil {
				t.Fatalf("encode trimmed: %v", err)
			}
			back, err := Decode(b, testLimits)
			if err != nil {
				t.Fatal(err)
			}
			if err := back.ValidateFor(node); err != nil {
				t.Fatal(err)
			}
			if !back.MultiRecipient == (n > 1) {
				t.Fatalf("multi flag changed by trimming: %v vs %d recipients", back.MultiRecipient, n)
			}
			if !e.DiscloseRecipients && len(back.To) != len(e.RecipientsOn(node)) {
				t.Fatalf("undisclosed envelope shows %d recipients, node has %d", len(back.To), len(e.RecipientsOn(node)))
			}
			for ep, n := range back.Seq {
				if e.Seq[ep] != n {
					t.Fatalf("trimming changed the sequence number of %v: %d, was %d", ep, n, e.Seq[ep])
				}
			}
			total += len(back.Seq)
		}
		if total != n {
			t.Fatalf("sequence numbers across the trimmed envelopes: %d, want %d", total, n)
		}
	}
	if disclosed < 100 || undisclosed < 100 || multiNodes < 100 || singleNode < 20 {
		t.Fatalf("paths: disclosed %d undisclosed %d multi-node %d single-node %d", disclosed, undisclosed, multiNodes, singleNode)
	}
	t.Logf("disclosed %d undisclosed %d multi-node %d single-node %d", disclosed, undisclosed, multiNodes, singleNode)
}

func TestRequestDigest(t *testing.T) {
	base := baseCreate(t)
	d0 := base.Digest()
	same := map[string]func(*Envelope){
		"another send time":      func(e *Envelope) { e.SentAt = e.SentAt.Add(time.Hour) },
		"another sequence":       func(e *Envelope) { e.Seq = map[ident.EndpointID]uint64{ep(2): 99} },
		"evidence added":         func(e *Envelope) { e.Evidence = []record.Record{mustDecodeRecord(t, goldenReportDelivered)} },
		"another trimmed list":   func(e *Envelope) { e.To = append(e.To, addr(nodeOf(t, keyIdentityB), 5)) },
		"another message id":     func(e *Envelope) { e.ID = "ctl-9999" },
		"another recipient node": func(e *Envelope) { e.To = []ident.Address{addr(nodeOf(t, keyIdentityB), 2)} },
	}
	for name, mut := range same {
		t.Run("a retry may differ in "+name, func(t *testing.T) {
			e := base
			mut(&e)
			if e.Digest() != d0 {
				t.Error("digest changed")
			}
		})
	}
	differs := map[string]func(*Envelope){
		"kind":                     func(e *Envelope) { e.Kind = KindMessage },
		"preset":                   func(e *Envelope) { e.Preset = "fast" },
		"content type":             func(e *Envelope) { e.ContentType = "text/markdown" },
		"body":                     func(e *Envelope) { e.Body = []byte("start there") },
		"empty body":               func(e *Envelope) { e.Body, e.ContentType = nil, "" },
		"accept_before":            func(e *Envelope) { e.AcceptBefore = e.AcceptBefore.Add(time.Second) },
		"report_by":                func(e *Envelope) { e.ReportBy = e.ReportBy.Add(time.Second) },
		"body moved to preset":     func(e *Envelope) { e.Preset, e.Body = "default"+string(e.Body), nil },
		"reply_to":                 func(e *Envelope) { e.ReplyTo = "other" },
		"thread":                   func(e *Envelope) { e.Thread = "elsewhere" },
		"multi-recipient flag":     func(e *Envelope) { e.MultiRecipient = true },
		"disclose_recipients":      func(e *Envelope) { e.DiscloseRecipients = true },
		"thread moved to reply_to": func(e *Envelope) { e.Thread, e.ReplyTo = "", "same" },
	}
	t.Run("when recipients are disclosed the digest covers the whole recipient list, in order", func(t *testing.T) {
		nodeA, nodeB := nodeOf(t, keyIdentityA), nodeOf(t, keyIdentityB)
		disclosed := base
		disclosed.DiscloseRecipients, disclosed.MultiRecipient = true, true
		disclosed.To = []ident.Address{addr(nodeA, 2), addr(nodeB, 5)}
		d := disclosed.Digest()
		for name, to := range map[string][]ident.Address{
			"a recipient added":            {addr(nodeA, 2), addr(nodeB, 5), addr(nodeB, 6)},
			"a recipient removed":          {addr(nodeA, 2)},
			"the order changed":            {addr(nodeB, 5), addr(nodeA, 2)},
			"another endpoint":             {addr(nodeA, 2), addr(nodeB, 6)},
			"another node for an endpoint": {addr(nodeA, 2), addr(nodeA, 5)},
		} {
			e := disclosed
			e.To = to
			if e.Digest() == d {
				t.Errorf("%s: the digest did not change", name)
			}
		}
		again := disclosed
		again.To = slices.Clone(disclosed.To)
		again.SentAt = disclosed.SentAt.Add(time.Hour)
		if again.Digest() != d {
			t.Error("the same disclosed list under another send time changed the digest")
		}
	})
	t.Run("the digest keeps the two flags apart", func(t *testing.T) {
		multi, disclose := base, base
		multi.MultiRecipient, disclose.DiscloseRecipients = true, true
		if multi.Digest() == disclose.Digest() {
			t.Error("multi-recipient and disclose_recipients give the same digest")
		}
	})
	for name, mut := range differs {
		t.Run("the digest covers the "+name, func(t *testing.T) {
			e := base
			mut(&e)
			if e.Digest() == d0 {
				t.Error("digest unchanged")
			}
		})
	}
}

// A caller reports a body over the limit to the sender as too large and one
// that is inconsistent as malformed, so the two are told apart by the error.
func TestATooLargeBodyIsToldApartFromAMalformedOne(t *testing.T) {
	small := Limits{MaxBody: 4, MaxRecipients: 64}
	_, err := Decode(goldenEnvelopeMessage, small)
	requireErr(t, err, ErrBodyTooLarge, ErrBody)

	exact := Limits{MaxBody: len("hello"), MaxRecipients: 64}
	if _, err := Decode(goldenEnvelopeMessage, exact); err != nil {
		t.Errorf("a body of exactly MaxBody bytes: %v", err)
	}

	cases := []struct {
		rule  string
		build func() envelopeWire
		mut   func(*envelopeWire)
	}{
		{"a content type without a body", func() envelopeWire { return wireOf(t, baseMessage(t)) }, func(w *envelopeWire) { w.Body = nil }},
		{"a body without a content type", func() envelopeWire { return wireOf(t, baseMessage(t)) }, func(w *envelopeWire) { w.ContentType = "" }},
		{"a content type that is not printable", func() envelopeWire { return wireOf(t, baseMessage(t)) }, func(w *envelopeWire) { w.ContentType = "text/\x01" }},
		{"a message without a body", func() envelopeWire { return wireOf(t, baseMessage(t)) }, func(w *envelopeWire) { w.Body, w.ContentType = nil, "" }},
		{"a body on a session.close", func() envelopeWire { return wireOf(t, baseCreate(t)) }, func(w *envelopeWire) {
			w.Kind, w.Params = wireKindSessionClose, nil
		}},
	}
	for _, c := range cases {
		w := c.build()
		c.mut(&w)
		err := decodeWire(t, w, testLimits)
		requireErr(t, err, ErrBody)
		if errors.Is(err, ErrBodyTooLarge) {
			t.Errorf("%s: reported as too large: %v", c.rule, err)
		}
	}
}

// A body is text: the envelope refuses one that is not valid UTF-8, on the sender's
// own node when it builds the envelope (Encode) and on the target when it reads one
// (Decode), as a malformed body and not a too large one.
func TestABodyMustBeValidUTF8(t *testing.T) {
	cases := []struct {
		name string
		base func(testing.TB) Envelope
		body string
		ok   bool
	}{
		{"ASCII", baseMessage, "hello", true},
		{"multi-byte text", baseMessage, "w\u00f6rld \u2603", true},
		{"a stray continuation byte", baseMessage, "ab\x80cd", false},
		{"a truncated multi-byte rune", baseMessage, "caf\xc3", false},
		{"an encoded surrogate", baseMessage, "\xed\xa0\x80", false},
		{"the initial message of a session.create, valid", baseCreate, "start", true},
		{"the initial message of a session.create, invalid", baseCreate, "st\xffart", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := c.base(t)
			e.Body = []byte(c.body)
			b, encErr := e.Encode(testLimits)
			w := wireOf(t, e)
			decErr := decodeWire(t, w, testLimits)
			if c.ok {
				requireNoErr(t, encErr)
				requireNoErr(t, decErr)
				if _, err := Decode(b, testLimits); err != nil {
					t.Errorf("what Encode wrote does not decode: %v", err)
				}
				return
			}
			for side, err := range map[string]error{"Encode": encErr, "Decode": decErr} {
				requireErr(t, err, ErrBody)
				if errors.Is(err, ErrBodyTooLarge) {
					t.Errorf("%s: reported as too large: %v", side, err)
				}
			}
		})
	}
}

func TestRetryAndDeduplication(t *testing.T) {
	nodeA, nodeB := nodeOf(t, keyIdentityA), nodeOf(t, keyIdentityB)
	first := baseCreate(t)
	key := func(e Envelope, to ident.EndpointID) delivery.DedupKey {
		return delivery.DedupKey{SenderNode: nodeB, Message: e.ID, Recipient: to}
	}
	rec := delivery.NewDedupRecord(key(first, ep(2)), first.Digest(), first.ReportBy)

	t.Run("the same request again, on another path, is a replay", func(t *testing.T) {
		again := first
		again.SentAt = first.SentAt.Add(time.Minute)
		again.Seq = map[ident.EndpointID]uint64{ep(2): 8}
		if v := delivery.CheckDedup(&rec, again.Digest()); v != delivery.DedupReplay {
			t.Errorf("verdict %v, want replay", v)
		}
	})
	t.Run("the same id with another body is a conflict", func(t *testing.T) {
		other := first
		other.Body = []byte("something else")
		if v := delivery.CheckDedup(&rec, other.Digest()); v != delivery.DedupConflict {
			t.Errorf("verdict %v, want conflict", v)
		}
	})
	t.Run("the same id with another preset is a conflict", func(t *testing.T) {
		other := first
		other.Preset = "fast"
		if v := delivery.CheckDedup(&rec, other.Digest()); v != delivery.DedupConflict {
			t.Errorf("verdict %v, want conflict", v)
		}
	})
	t.Run("a message.send under the id of the initial message conflicts with the derived delivery", func(t *testing.T) {
		derived := delivery.NewDedupRecord(key(first, ep(3)), first.Digest(), first.ReportBy)
		forged := first
		forged.Kind, forged.Preset = KindMessage, ""
		forged.To = []ident.Address{addr(nodeA, 3)}
		if v := delivery.CheckDedup(&derived, forged.Digest()); v != delivery.DedupConflict {
			t.Errorf("verdict %v, want conflict", v)
		}
	})
	t.Run("the stored control reply is kept once", func(t *testing.T) {
		stored, err := rec.WithReply([]byte("reply"))
		requireNoErr(t, err)
		if _, err := stored.WithReply([]byte("another")); err == nil {
			t.Error("a second reply was stored")
		}
	})
	t.Run("the dedup record of a retry is kept until report_by", func(t *testing.T) {
		if !rec.RetainUntil.Equal(first.ReportBy) {
			t.Errorf("retained until %v, want %v", rec.RetainUntil, first.ReportBy)
		}
	})
}

// The system endpoint is the sender of a node's own channel feedback, so no
// message may be addressed to it, whichever way the envelope is built.
func TestSystemEndpointIsNoRecipient(t *testing.T) {
	system := ident.SystemEndpoint()

	t.Run("the reason is a recipient error", func(t *testing.T) {
		if !errors.Is(ErrSystemRecipient, ErrRecipients) {
			t.Fatal("ErrSystemRecipient does not wrap ErrRecipients")
		}
	})
	t.Run("Encode refuses an envelope addressed to it", func(t *testing.T) {
		e := baseMessage(t)
		e.To[0].Endpoint = system
		e.Seq = map[ident.EndpointID]uint64{system: 1}
		_, err := e.Encode(testLimits)
		requireErr(t, err, ErrSystemRecipient, ErrRecipients)
	})
	t.Run("a neighbouring identifier is a recipient like any other", func(t *testing.T) {
		near := system.Bytes()
		near[len(near)-1] = 0xfe
		nearID, err := ident.EndpointIDFromBytes(near)
		requireNoErr(t, err)
		e := baseMessage(t)
		e.To[0].Endpoint = nearID
		e.Seq = map[ident.EndpointID]uint64{nearID: 1}
		b, err := e.Encode(testLimits)
		requireNoErr(t, err)
		_, err = Decode(b, testLimits)
		requireNoErr(t, err)
	})
}

func TestValidMessageIDIsTheRuleTheDecoderAppliesToIds(t *testing.T) {
	cases := []struct {
		rule string
		id   string
		ok   bool
	}{
		{"one visible byte is an id", "a", true},
		{"the lowest visible byte is accepted", "!", true},
		{"the highest visible byte is accepted", "~", true},
		{"an id of MaxIDLen bytes is accepted", strings.Repeat("x", MaxIDLen), true},
		{"punctuation is part of an id", `ctl-0001/a.b:c"d'e`, true},
		{"an id is not empty", "", false},
		{"an id is at most MaxIDLen bytes", strings.Repeat("x", MaxIDLen+1), false},
		{"an id has no space", "a b", false},
		{"an id has no leading space", " a", false},
		{"an id has no control byte", "a\x07b", false},
		{"an id has no newline", "a\nb", false},
		{"an id has no delete byte", "a\x7fb", false},
		{"an id is ASCII", "mé", false},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			err := ValidMessageID(delivery.MessageID(c.id))
			if c.ok != (err == nil) {
				t.Fatalf("ValidMessageID(%q) = %v, want ok=%v", c.id, err, c.ok)
			}
			if err != nil && !errors.Is(err, ErrID) {
				t.Fatalf("ValidMessageID(%q) = %v, want an error wrapping ErrID", c.id, err)
			}
			// The decoder is the authority: an id decodes in an envelope exactly when it is valid.
			w := wireOf(t, baseMessage(t))
			w.ID = c.id
			_, derr := fromWire(w, testLimits)
			if c.ok != (derr == nil) {
				t.Fatalf("fromWire with id %q = %v, but ValidMessageID said ok=%v", c.id, derr, c.ok)
			}
		})
	}
}
