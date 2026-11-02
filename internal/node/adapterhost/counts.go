package adapterhost

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ConnCounts is what the host counts of the connection of one instance, for the
// caller that asks whether the system is quiet.
type ConnCounts struct {
	// Connected is whether a connection past adapter.start exists.
	Connected bool
	// Handled is how many notifications of the connection the engine has finished
	// with, and Buffered how many the host has received and the engine has not
	// finished with. The adapter says how many it sent; the system is quiet only
	// when Handled equals that and Buffered is zero.
	Handled  int
	Buffered int
	// Requests is how many requests of the adapter are being answered now.
	Requests int
	// Calls lists the identities (see CallID) of the requests the host has sent the
	// adapter and has not yet taken the answer to, one entry per request, sorted. A
	// ping is among them while it has no answer: the host is then parked with a
	// timeout armed, and a caller that moved the test clock past it would make the
	// ping fail. A request leaves the list only when the host has taken its
	// answer, so a host that is idle with none listed has acted on every answer.
	Calls []string
	// Working is whether the host is in the middle of something it does on its
	// own: handling the end of a connection, starting an instance, taking one on,
	// answering a timer. It is also true while a timer of the host is due and
	// not yet taken, which is how a caller that moved the test clock finds out
	// that the host has not yet reacted to it.
	Working bool
}

// CallID identifies one request of the host to an adapter by what it asked: the
// method, a "#", and the first 16 hex digits of a SHA-256 over the method, a zero
// byte and the request's params in a canonical form (the JSON value decoded and
// encoded again with its numbers kept as written, so that the escaping and the
// order of the encoder that put the params on the wire do not matter). The
// request's own id on the wire is the peer's and not visible here; two requests of
// one method are told apart by their params, which carry the endpoint and the
// attempt. A caller that must tell which of the requests in flight an adapter is
// holding on purpose compares CallIDs, never methods: after one request of a
// method was cancelled, another of the same method may be in flight while the
// adapter still holds the first.
//
// A third party computes the same identity from the params it received. The fake
// adapter of the test kit does, in fakeadapter.RequestID, a copy of this function
// that must change with it (the fake may not import the host); a test of this
// package checks that the two agree.
//
// The identity assumes that the engine never has two requests with equal params
// outstanding on one connection: two such requests have one identity and cannot be
// told apart.
func CallID(method string, params []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write(canonicalJSON(params))
	return method + "#" + hex.EncodeToString(h.Sum(nil)[:8])
}

// canonicalJSON returns raw as one JSON value with sorted object keys, no
// insignificant space and the escaping of encoding/json, numbers as written; no
// params and null are {}, as the peer sends them. A value that is not JSON is
// returned as it is. fakeadapter.canonicalJSON is a copy and changes with it.
func canonicalJSON(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte(`{}`)
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	if v == nil {
		return []byte(`{}`)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// callIDOf is the CallID of a request about to be sent with params. A request
// whose params cannot be encoded is never sent, and carries the identity of its
// method alone.
func callIDOf(method string, params adapterproto.Message) string {
	raw, err := json.Marshal(params)
	if err != nil {
		return method + "#"
	}
	return CallID(method, raw)
}
