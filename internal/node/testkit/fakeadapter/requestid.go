package fakeadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// requestIDKey is the context key of the identity of the request a handler serves.
type requestIDKey struct{}

// withRequestID returns ctx carrying the identity of the request of the method
// with the params raw, so that whatever holds the request on purpose can report
// it without the handler passing it down.
func withRequestID(ctx context.Context, method string, raw json.RawMessage) context.Context {
	return context.WithValue(ctx, requestIDKey{}, RequestID(method, raw))
}

// requestIDOf is the identity of the request ctx belongs to.
func requestIDOf(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestID identifies a request by what it asked, which is all a peer sees of it
// from the sdk: the method, a "#", and the first 16 hex digits of a SHA-256 over
// the method, a zero byte and the params in a canonical form (the JSON value
// decoded and encoded again with its numbers kept as written). The daemon's
// adapter host builds the same string for the requests it has in flight, so a
// fixture compares the two sets; the encoder of the host decides the escaping and
// the key order on the wire, and the canonical form makes that not matter. A
// request without params is one with {}, as the peer sends it.
//
// This is a copy of adapterhost.CallID and must change with it: the fake is a
// third party and may import only the standard library, sdk/go and the other
// packages of the third-party side, so it cannot call the host's function, and
// a test in the adapter host compares the two. Like CallID it assumes that the
// daemon never has two requests with equal params outstanding on one
// connection: two such requests have one identity and cannot be told apart.
func RequestID(method string, raw json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write(canonicalJSON(raw))
	return method + "#" + hex.EncodeToString(h.Sum(nil)[:8])
}

// canonicalJSON returns raw as one JSON value with sorted object keys, no
// insignificant space and the escaping of encoding/json, numbers as written; no
// params and null are {}, as the peer sends them. A value that is not JSON is
// returned as it is. It is a copy of the function of the same name in
// adapterhost and changes with it.
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
