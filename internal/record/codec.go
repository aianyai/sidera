package record

import (
	"bytes"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// DefaultMaxInput is the starting input-size limit for a whole record. The
// limit is a parameter of every decoding entry point so that a record type with
// larger content can ask for more.
const DefaultMaxInput = 64 << 10

var (
	encMode = mustEncMode()
	decMode = mustDecMode()
)

// mustEncMode returns RFC 8949 core deterministic encoding (shortest integers,
// no indefinite lengths, map keys sorted by their encoded bytes), with nil
// slices written as empty byte strings so that "null" never stands for an
// empty value, and with tags refused.
func mustEncMode() cbor.EncMode {
	opts := cbor.CoreDetEncOptions()
	opts.NilContainers = cbor.NilContainerAsEmpty
	opts.TagsMd = cbor.TagsForbidden
	m, err := opts.EncMode()
	if err != nil {
		panic("record: encoding options: " + err.Error())
	}
	return m
}

// mustDecMode returns the hardened decoding options. The library's defaults
// silently keep one of two duplicate map keys and accept indefinite lengths and
// tags, so every one of them is switched off here. Nesting stays at the
// library's default bound of 32 levels; every record is decoded into a typed
// struct, so deeper input is refused by its types long before that matters.
func mustDecMode() cbor.DecMode {
	m, err := cbor.DecOptions{
		DupMapKey:         cbor.DupMapKeyEnforcedAPF,
		IndefLength:       cbor.IndefLengthForbidden,
		TagsMd:            cbor.TagsForbidden,
		ExtraReturnErrors: cbor.ExtraDecErrorUnknownField,
		NaN:               cbor.NaNDecodeForbidden,
		Inf:               cbor.InfDecodeForbidden,
	}.DecMode()
	if err != nil {
		panic("record: decoding options: " + err.Error())
	}
	return m
}

// MarshalCanonical encodes v, a struct with integer-keyed fields (or a value
// built from them), as deterministic CBOR.
func MarshalCanonical(v any) ([]byte, error) {
	b, err := encMode.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("record: encode: %w", err)
	}
	return b, nil
}

// UnmarshalStrict decodes data into v, which must be a pointer to a struct with
// integer-keyed fields. It rejects input longer than maxLen, applies the
// hardened decoding options, and then encodes the result again: unless the
// bytes are identical, the input was not the unique deterministic encoding and
// is refused. That last step is what rejects non-shortest integers, unsorted
// map keys and a null or non-minimal form where a canonical one exists.
//
// Fields left out of the input decode to their zero value and are written by
// the encoder, so a required field that is missing makes the bytes differ and
// is refused as well; only fields tagged omitempty may be absent.
func UnmarshalStrict(data []byte, v any, maxLen int) error {
	if maxLen <= 0 || len(data) > maxLen {
		return fmt.Errorf("%w: %d bytes, limit %d", ErrOversize, len(data), maxLen)
	}
	if err := decMode.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	again, err := encMode.Marshal(v)
	if err != nil {
		return fmt.Errorf("record: re-encode: %w", err)
	}
	if !bytes.Equal(again, data) {
		return ErrNotCanonical
	}
	return nil
}
