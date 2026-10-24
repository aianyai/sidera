package keys

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The kinds of the files in the record envelope.
const (
	kindIdentityKey = "identity-key"
	kindNetworkKey  = "network-key"
	kindSerial      = "serial"
	kindClock       = "clock"
	kindInit        = "init"
)

// timeLayout is the one text of a time in these files: UTC, whole seconds.
const timeLayout = "2006-01-02T15:04:05Z"

func formatTime(t time.Time) string { return t.UTC().Truncate(time.Second).Format(timeLayout) }

// parseTime reads a UTC time in whole seconds from the epoch on. A spelling that
// parses but is not what formatTime writes (a one-digit hour) is refused by the
// canonical check of the body.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil || t.Unix() < 1 {
		return time.Time{}, errors.New("not a UTC time in whole seconds from 1970 on")
	}
	return t, nil
}

// marshal encodes a body that holds only strings and counters, which cannot
// fail.
func marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("keys: encoding a body cannot fail: " + err.Error())
	}
	return b
}

// decodeBody decodes the body of the file into v with the strict rules and then
// checks that encoding what was decoded gives the same body: a body is read only
// in the one form it is written. The first line is the envelope's to check.
func decodeBody(kind string, data []byte, v any, build func() ([]byte, error)) error {
	body, err := decodeEnvelope(kind, data)
	if err != nil {
		return err
	}
	if err := strictjson.Decode(body, v); err != nil {
		return fmt.Errorf("the body is not valid: %w", err)
	}
	again, err := build()
	if err != nil {
		return err
	}
	if !bytes.Equal(again, body) {
		return errors.New("the body is not in the canonical form")
	}
	return nil
}

// ---- private keys ----

type keyBody struct {
	Algorithm string `json:"algorithm"`
	Seed      string `json:"seed"`
}

const algorithmEd25519 = "ed25519"

func keyBodyOf(seed [32]byte) []byte {
	return marshal(keyBody{Algorithm: algorithmEd25519, Seed: base64.RawURLEncoding.EncodeToString(seed[:])})
}

func encodeKeyFile(kind string, seed [32]byte) []byte { return encodeEnvelope(kind, keyBodyOf(seed)) }

// decodeKeyFile returns the seed of a key file. Every error is a fixed text: a
// key file is secret, and what is wrong with it is never quoted.
func decodeKeyFile(kind string, data []byte) (seed [32]byte, err error) {
	body, err := decodeEnvelope(kind, data)
	if err != nil {
		return seed, err
	}
	var b keyBody
	if strictjson.Decode(body, &b) != nil {
		return seed, errors.New("the body is not a key")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(b.Seed)
	if b.Algorithm != algorithmEd25519 || err != nil || len(raw) != len(seed) {
		return seed, errors.New("the body is not an Ed25519 seed")
	}
	copy(seed[:], raw)
	if !bytes.Equal(keyBodyOf(seed), body) {
		return seed, errors.New("the body is not in the canonical form")
	}
	return seed, nil
}

// ---- the serial ----

type serialBody struct {
	Node   string `json:"node"`
	Serial uint64 `json:"serial"`
	Digest string `json:"digest"`
}

func serialBodyOf(s record.SerialSeen) []byte {
	return marshal(serialBody{Node: s.Node.String(), Serial: s.Serial, Digest: s.Digest.String()})
}

func encodeSerial(s record.SerialSeen) []byte { return encodeEnvelope(kindSerial, serialBodyOf(s)) }

func decodeSerial(data []byte) (record.SerialSeen, error) {
	var b serialBody
	var s record.SerialSeen
	err := decodeBody(kindSerial, data, &b, func() ([]byte, error) {
		node, err := ident.ParseNodeID(b.Node)
		if err != nil {
			return nil, fmt.Errorf("node: %w", err)
		}
		if err := record.RequireCounter(b.Serial); err != nil {
			return nil, fmt.Errorf("serial: %w", err)
		}
		raw, err := hex.DecodeString(b.Digest)
		if err != nil || len(raw) != len(s.Digest) {
			return nil, errors.New("digest is not 32 bytes of hex")
		}
		copy(s.Digest[:], raw)
		if s.Digest.IsZero() {
			return nil, errors.New("digest is zero")
		}
		s.Node, s.Serial = node, b.Serial
		return serialBodyOf(s), nil
	})
	if err != nil {
		return record.SerialSeen{}, err
	}
	return s, nil
}

// ---- the clock ----

type clockBody struct {
	HighWater           string `json:"high_water"`
	RollbackPausedSince string `json:"rollback_paused_since,omitempty"`
}

func clockBodyOf(c ClockState) []byte {
	b := clockBody{HighWater: formatTime(c.HighWater)}
	if !c.RollbackPausedSince.IsZero() {
		b.RollbackPausedSince = formatTime(c.RollbackPausedSince)
	}
	return marshal(b)
}

func encodeClock(c ClockState) []byte { return encodeEnvelope(kindClock, clockBodyOf(c)) }

func decodeClock(data []byte) (ClockState, error) {
	var b clockBody
	var c ClockState
	err := decodeBody(kindClock, data, &b, func() ([]byte, error) {
		var err error
		if c.HighWater, err = parseTime(b.HighWater); err != nil {
			return nil, fmt.Errorf("high_water: %w", err)
		}
		if b.RollbackPausedSince != "" {
			if c.RollbackPausedSince, err = parseTime(b.RollbackPausedSince); err != nil {
				return nil, fmt.Errorf("rollback_paused_since: %w", err)
			}
		}
		return clockBodyOf(c), nil
	})
	if err != nil {
		return ClockState{}, err
	}
	return c, nil
}

// ---- the init marker ----

type initBody struct {
	CompletedAt string `json:"completed_at"`
}

func initBodyOf(t time.Time) []byte { return marshal(initBody{CompletedAt: formatTime(t)}) }

func encodeInit(t time.Time) []byte { return encodeEnvelope(kindInit, initBodyOf(t)) }

func decodeInit(data []byte) (time.Time, error) {
	var b initBody
	var at time.Time
	err := decodeBody(kindInit, data, &b, func() ([]byte, error) {
		var err error
		if at, err = parseTime(b.CompletedAt); err != nil {
			return nil, fmt.Errorf("completed_at: %w", err)
		}
		return initBodyOf(at), nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return at, nil
}
