package reply

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var t0 = time.Unix(1700000000, 0).UTC()

// Seeds: 1 is the RFC 8032 test key 1, any other n the byte n repeated 32 times.
// Node A is the target of the messages, node B the sender's node, node C a
// third party. Each node has an identity key and a network key.
const (
	idA, netA = 1, 0x11
	idB, netB = 0x44, 0x55
	idC, netC = 0x66, 0x77
)

func signerOf(n int) record.Signer {
	seed := bytes.Repeat([]byte{byte(n)}, ed25519.SeedSize)
	if n == 1 {
		seed, _ = hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	}
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		panic(err)
	}
	return s
}

func nodeOf(n int) ident.NodeID {
	id, err := ident.NodeIDFromKey(signerOf(n).Public())
	if err != nil {
		panic(err)
	}
	return id
}

func ep(b byte) ident.EndpointID {
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{b}, ident.EndpointIDLen))
	if err != nil {
		panic(err)
	}
	return id
}

// acceptedFor is the certificate a verifier holds for a node: the network key
// of netSeed, valid from a day before t0 for 60 days, not revoked.
func acceptedFor(idSeed, netSeed int) record.AcceptedNetworkKey {
	return record.AcceptedNetworkKey{
		Cert: &record.NetworkKeyCertificate{
			Node: nodeOf(idSeed), NetworkKey: signerOf(netSeed).Public(), Serial: 1,
			NotBefore: t0.Add(-24 * time.Hour), NotAfter: t0.Add(60 * 24 * time.Hour),
		},
		Revocation: record.KeyRevocationClear,
	}
}

// acceptedOn is the accepted network key for whichever of the cast's nodes the
// address is on.
func acceptedOn(a ident.Address) record.AcceptedNetworkKey {
	for _, c := range [][2]int{{idA, netA}, {idB, netB}, {idC, netC}} {
		if nodeOf(c[0]) == a.Node {
			return acceptedFor(c[0], c[1])
		}
	}
	return record.AcceptedNetworkKey{}
}

func requireErr(t testing.TB, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}

func requireNoErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func senderEndpoint() ident.EndpointID { return ep(0x0a) }

// The cast of the scenarios: the sender's endpoint is 0x0a on node B; the
// message goes to endpoint 2 on node A (a plain endpoint, or a runtime
// endpoint when the message is a session.create); the session it creates is
// endpoint 3 on node A; node C is a third party.
func onA(b byte) ident.Address { return ident.Address{Node: nodeOf(idA), Endpoint: ep(b)} }
func onC(b byte) ident.Address { return ident.Address{Node: nodeOf(idC), Endpoint: ep(b)} }

var (
	accept   = t0.Add(10 * time.Minute)
	runtimeR = ep(2)
	sessionD = ep(3)
)

func queued() delivery.Record { return delivery.NewSourceRecord(accept) }

// messageOutbound is the sender's record of a plain message "m-1" to endpoint
// 2 of node A, nothing reported yet.
func messageOutbound() envelope.Outbound {
	return envelope.Outbound{
		Message: "m-1", From: senderEndpoint(), Kind: envelope.KindMessage, BodyNonEmpty: true, AcceptBefore: accept,
		Recipients: []envelope.Tracked{{Address: onA(2), Delivery: queued()}},
	}
}

// createOutbound is the sender's record of a session.create "m-1" to runtime
// endpoint 2 of node A.
func createOutbound(body bool) envelope.Outbound {
	o := messageOutbound()
	o.Kind, o.BodyNonEmpty = envelope.KindSessionCreate, body
	return o
}

func sealReport(t testing.TB, msg delivery.MessageID, to ident.EndpointID, st delivery.StatusReport, reported, deliveredAt time.Time, idSeed, netSeed int) record.Record {
	t.Helper()
	d := envelope.StatusReportDescriptor(nodeOf(idSeed), acceptedFor(idSeed, netSeed), t0)
	r, err := record.Seal(d, envelope.StatusReport{Sender: nodeOf(idB), Message: msg, Recipient: to, Status: st, ReportedAt: reported, DeliveredAt: deliveredAt}, signerOf(netSeed))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// sealReportFrom signs a report of the target node A that names another sending
// node.
func sealReportFrom(t testing.TB, sender ident.NodeID, msg delivery.MessageID, to ident.EndpointID, st delivery.StatusReport, reported time.Time) record.Record {
	t.Helper()
	d := envelope.StatusReportDescriptor(nodeOf(idA), acceptedFor(idA, netA), t0)
	r, err := record.Seal(d, envelope.StatusReport{Sender: sender, Message: msg, Recipient: to, Status: st, ReportedAt: reported}, signerOf(netA))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// sealCreateReplyFrom signs a control reply of the target node A that names
// another sending node.
func sealCreateReplyFrom(t testing.TB, sender ident.NodeID, res envelope.CreateResult, issued time.Time) record.Record {
	t.Helper()
	d := envelope.ControlReplyDescriptor(nodeOf(idA), acceptedFor(idA, netA), t0)
	r, err := record.Seal(d, envelope.ControlReply{
		Sender: sender, Message: "m-1", Recipient: runtimeR, IssuedAt: issued,
		Result: envelope.ControlResult{Signal: adapterproto.SignalSessionCreate, Create: &res},
	}, signerOf(netA))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func acceptedReport(t testing.TB, to ident.EndpointID, reported time.Time) record.Record {
	return sealReport(t, "m-1", to, delivery.StatusReport{State: delivery.StateAccepted}, reported, time.Time{}, idA, netA)
}

func deliveredReport(t testing.TB, to ident.EndpointID, reported, deliveredAt time.Time) record.Record {
	return sealReport(t, "m-1", to, delivery.StatusReport{State: delivery.StateDelivered}, reported, deliveredAt, idA, netA)
}

func sealCreateReply(t testing.TB, msg delivery.MessageID, res envelope.CreateResult, issued time.Time, idSeed, netSeed int) record.Record {
	t.Helper()
	d := envelope.ControlReplyDescriptor(nodeOf(idSeed), acceptedFor(idSeed, netSeed), t0)
	r, err := record.Seal(d, envelope.ControlReply{
		Sender: nodeOf(idB), Message: msg, Recipient: runtimeR, IssuedAt: issued,
		Result: envelope.ControlResult{Signal: adapterproto.SignalSessionCreate, Create: &res},
	}, signerOf(netSeed))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func createdReply(t testing.TB, issued time.Time) record.Record {
	return sealCreateReply(t, "m-1", envelope.CreateResult{Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: sessionD}, issued, idA, netA)
}

func failedReply(t testing.TB, issued time.Time) record.Record {
	return sealCreateReply(t, "m-1", envelope.CreateResult{Outcome: adapterproto.CreateReplyFailed, Reason: adapterproto.CreateReasonNativeError}, issued, idA, netA)
}

// request is a reply that arrived at now from replier to the sender's endpoint.
func request(o *envelope.Outbound, replier ident.Address, now time.Time, evidence ...record.Record) Request {
	return Request{
		Now: now, Enabled: true, Local: nodeOf(idB), Replier: replier, Target: senderEndpoint(), ReplyTo: "m-1",
		Outbound: o, Evidence: evidence, Accepted: acceptedOn(replier),
	}
}

func mustAdmit(t testing.TB, req Request) Admission {
	t.Helper()
	adm, err := Admit(req)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return adm
}

// learn applies a status report the sender pulled, as the pull path does.
func learn(t testing.TB, o envelope.Outbound, rec record.Record, now time.Time) envelope.Outbound {
	t.Helper()
	sr, err := envelope.VerifyStatusReport(rec, nodeOf(idA), nodeOf(idB), acceptedFor(idA, netA), now)
	requireNoErr(t, err)
	n, _, _, err := o.ApplyReport(now, sr)
	requireNoErr(t, err)
	return n
}

// learnReply applies a control reply the sender pulled, as the pull path does.
func learnReply(t testing.TB, o envelope.Outbound, rec record.Record, now time.Time) envelope.Outbound {
	t.Helper()
	sc, err := envelope.VerifyControlReply(rec, nodeOf(idA), nodeOf(idB), acceptedFor(idA, netA), now)
	requireNoErr(t, err)
	h, err := envelope.HandleControlReply(now, o, sc, nil)
	requireNoErr(t, err)
	return h.Outbound
}
