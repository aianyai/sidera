package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// MaxAcceptFrom is the most senders an endpoint can list in its declaration. The
// declaration is part of the endpoint's record, and every change to the endpoint
// rewrites and syncs that record whole, so the size of one record is what bounds
// the list: an address is about eighty characters, and a list at this bound is
// some twenty kilobytes, already longer than anyone writes by hand. It is one
// constant so that the number changes in one place; the engine owns the encoding
// of the declaration and so this bound with it.
const MaxAcceptFrom = 256

// The declaration of an endpoint is the field of its record that the engine owns:
// one compact JSON object with a fixed member order. A text that is not exactly
// what encodeDeclaration writes for the value it decodes to is not read, so one
// value has one text. The display name and the description the adapter gave are
// fields of the record of their own.
type declarationWire struct {
	Paused               bool     `json:"paused,omitempty"`
	RefuseMultiRecipient bool     `json:"refuse_multi_recipient,omitempty"`
	RestrictSenders      bool     `json:"restrict_senders,omitempty"`
	AcceptFrom           []string `json:"accept_from,omitempty"`
	HiddenFromDirectory  bool     `json:"hidden_from_directory,omitempty"`
}

// senderListError is the fault of a list of senders, with the detail a refusal
// of a caller's declaration carries for it. It is not a Refusal itself:
// encodeDeclaration also judges the list when it reads a record back, and a
// fault there is damage to the record, which must not be read as a caller's
// refusal.
type senderListError struct {
	detail Detail
	msg    string
}

func (e *senderListError) Error() string { return e.msg }

// checkAcceptFrom judges the list of senders of a declaration: at most
// MaxAcceptFrom, each a real address, none twice. A fault of the list is a
// *senderListError; a zero address is a plain error, because no interface can
// send one, so it fails as the defect it would be.
func checkAcceptFrom(list []ident.Address) error {
	if len(list) > MaxAcceptFrom {
		return &senderListError{DetailTooManySenders, fmt.Sprintf("%d senders, at most %d", len(list), MaxAcceptFrom)}
	}
	seen := make(map[ident.Address]bool, len(list))
	for i, a := range list {
		switch {
		case a.IsZero():
			// Both interfaces parse an address before the engine sees it, so a zero
			// address is a defect of the caller, not a refusal.
			return fmt.Errorf("sender %d is the zero address", i)
		case seen[a]:
			return &senderListError{DetailDuplicateSender, fmt.Sprintf("sender %d is listed twice", i)}
		}
		seen[a] = true
	}
	return nil
}

// encodeDeclaration is the text of the value. It refuses what decodeDeclaration
// would.
func encodeDeclaration(d authz.Declaration) ([]byte, error) {
	if err := checkAcceptFrom(d.AcceptFrom); err != nil {
		return nil, fmt.Errorf("engine: declaration: %w", err)
	}
	w := declarationWire{
		Paused: d.Paused, RefuseMultiRecipient: d.RefuseMultiRecipient,
		RestrictSenders: d.RestrictSenders, HiddenFromDirectory: d.HiddenFromDirectory,
	}
	for _, a := range d.AcceptFrom {
		w.AcceptFrom = append(w.AcceptFrom, a.String())
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // the text stays what a person reads in the record
	if err := enc.Encode(w); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// decodeDeclaration reads the text of a value. Nothing at all declares nothing.
func decodeDeclaration(b []byte) (authz.Declaration, error) {
	if len(b) == 0 {
		return authz.Declaration{}, nil
	}
	var w declarationWire
	if err := strictjson.Decode(b, &w); err != nil {
		return authz.Declaration{}, fmt.Errorf("engine: declaration: %w", err)
	}
	d := authz.Declaration{
		Paused: w.Paused, RefuseMultiRecipient: w.RefuseMultiRecipient,
		RestrictSenders: w.RestrictSenders, HiddenFromDirectory: w.HiddenFromDirectory,
	}
	for _, s := range w.AcceptFrom {
		a, err := ident.ParseAddress(s)
		if err != nil {
			return authz.Declaration{}, fmt.Errorf("engine: declaration: accept_from: %w", err)
		}
		d.AcceptFrom = append(d.AcceptFrom, a)
	}
	again, err := encodeDeclaration(d)
	if err != nil {
		return authz.Declaration{}, err
	}
	if !bytes.Equal(again, b) {
		return authz.Declaration{}, errors.New("engine: declaration: not the canonical text of its value")
	}
	return d, nil
}
