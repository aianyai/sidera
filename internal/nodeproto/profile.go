package nodeproto

import "github.com/aianyai/sidera/internal/record"

// ProfileRequest asks the target for its current node profile. It carries
// nothing: the target answers with the profile it has issued for itself, which
// is the same for every asker the connection lets in, and the asking node is
// the one the connection authenticated.
type ProfileRequest struct{}

type profileRequestWire struct{}

// Encode returns the canonical bytes of the request, the empty map.
func (ProfileRequest) Encode() ([]byte, error) {
	return encodeChecked(profileRequestWire{}, DecodeProfileRequest)
}

// DecodeProfileRequest parses a profile request strictly, within the request
// limit of the protocol: it is the empty map and nothing else.
func DecodeProfileRequest(data []byte) (ProfileRequest, error) {
	var w profileRequestWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolProfile).Request); err != nil {
		return ProfileRequest{}, err
	}
	return ProfileRequest{}, nil
}

// ProfileAnswer is the target's answer to a profile request. With the outcome ok
// it holds the target's signed node profile, as the target signed it. A refused
// answer carries the reason name and a failed one nothing.
type ProfileAnswer struct {
	Outcome Outcome
	// Profile is present exactly when the outcome is ok. Only its shape is
	// checked here: whether it is a profile of the target, its signature and its
	// fields are the asker's to verify, with the certificate it holds for the
	// target.
	Profile *record.Record
	Reason  string
}

type profileAnswerWire struct {
	Outcome uint64 `cbor:"1,keyasint"`
	Profile []byte `cbor:"2,keyasint,omitempty"`
	Reason  string `cbor:"3,keyasint,omitempty"`
}

// Encode returns the canonical bytes of the answer. It refuses what
// DecodeProfileAnswer would.
func (a ProfileAnswer) Encode() ([]byte, error) {
	profile, err := encodeOptionalRecord(a.Profile)
	if err != nil {
		return nil, err
	}
	return encodeChecked(profileAnswerWire{Outcome: uint64(a.Outcome), Profile: profile, Reason: a.Reason}, DecodeProfileAnswer)
}

// DecodeProfileAnswer parses a profile answer strictly, within the answer limit
// of the protocol. The profile goes with the outcome ok and with no other, and
// an ok answer without one is invalid.
func DecodeProfileAnswer(data []byte) (ProfileAnswer, error) {
	var w profileAnswerWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolProfile).Answer); err != nil {
		return ProfileAnswer{}, err
	}
	o, err := checkOutcome(w.Outcome, w.Reason)
	if err != nil {
		return ProfileAnswer{}, err
	}
	if (o == OutcomeOK) != (len(w.Profile) > 0) {
		return ProfileAnswer{}, invalid("a profile goes with an ok outcome and with no other, outcome %s", o)
	}
	profile, err := decodeOptionalRecord(w.Profile, "profile")
	if err != nil {
		return ProfileAnswer{}, err
	}
	return ProfileAnswer{Outcome: o, Profile: profile, Reason: w.Reason}, nil
}
