package nodeproto

import (
	"fmt"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// encodeChecked writes the wire form of a message and runs it through its own
// decoder, so that what this package writes is what every receiver accepts and
// the rules of a message live in its decoder alone.
func encodeChecked[T any](wire any, decode func([]byte) (T, error)) ([]byte, error) {
	b, err := record.MarshalCanonical(wire)
	if err != nil {
		return nil, err
	}
	if _, err := decode(b); err != nil {
		return nil, err
	}
	return b, nil
}

// encodeRecords turns records into the byte strings a message carries.
func encodeRecords(recs []record.Record) ([][]byte, error) {
	out := make([][]byte, len(recs))
	for i, r := range recs {
		b, err := r.Encode()
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i, err)
		}
		out[i] = b
	}
	return out, nil
}

// decodeRecords parses the records of a list of at most max entries, each
// strictly and within the record limit. Nothing is verified.
func decodeRecords(list [][]byte, max int, what string) ([]record.Record, error) {
	if len(list) > max {
		return nil, invalid("%s: %d records, at most %d", what, len(list), max)
	}
	out := make([]record.Record, len(list))
	for i, b := range list {
		r, err := record.Decode(b, record.DefaultMaxInput)
		if err != nil {
			return nil, fmt.Errorf("%s %d: %w", what, i, err)
		}
		out[i] = r
	}
	return out, nil
}

// decodeOptionalRecord parses the record of an optional field; an absent field
// has no bytes.
func decodeOptionalRecord(b []byte, what string) (*record.Record, error) {
	if len(b) == 0 {
		return nil, nil
	}
	r, err := record.Decode(b, record.DefaultMaxInput)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return &r, nil
}

// encodeOptionalRecord is the inverse: no record is no bytes.
func encodeOptionalRecord(r *record.Record) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return r.Encode()
}

// parseEndpointText reads an endpoint identifier in its bech32m text form. The
// text must be the one form String writes: the form has a second spelling
// (upper case) that the strict re-encoding of the message cannot catch, because
// it compares the text, not the identifier.
func parseEndpointText(s, what string) (ident.EndpointID, error) {
	id, err := ident.ParseEndpointID(s)
	if err != nil {
		return ident.EndpointID{}, invalid("%s: %v", what, err)
	}
	if id.String() != s {
		return ident.EndpointID{}, invalid("%s: not the canonical text form", what)
	}
	return id, nil
}

// endpointsToWire writes endpoint identifiers as byte strings.
func endpointsToWire(ids []ident.EndpointID) [][]byte {
	out := make([][]byte, len(ids))
	for i, id := range ids {
		out[i] = id.Bytes()
	}
	return out
}

// endpointsFromWire reads a list of 1..max distinct endpoint identifiers, or
// of 0..max when empty is allowed.
func endpointsFromWire(list [][]byte, min, max int, what string) ([]ident.EndpointID, error) {
	if len(list) < min || len(list) > max {
		return nil, invalid("%s: %d endpoints, want %d..%d", what, len(list), min, max)
	}
	out := make([]ident.EndpointID, len(list))
	seen := make(map[ident.EndpointID]struct{}, len(list))
	for i, b := range list {
		id, err := ident.EndpointIDFromBytes(b)
		if err != nil {
			return nil, invalid("%s %d: %v", what, i, err)
		}
		if _, dup := seen[id]; dup {
			return nil, invalid("%s %d repeats an endpoint", what, i)
		}
		seen[id] = struct{}{}
		out[i] = id
	}
	return out, nil
}
