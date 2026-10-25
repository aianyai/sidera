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
// declaration is part of the endpoint's record, which is rewritten whole by every
// change, so it has to stay small: a node holds at most as many local endpoints
// as publication.MaxEndpointsPerNode, and a list of that length is already one
// that no agent writes by hand.
const MaxAcceptFrom = 256

// endpointMeta is what the engine owns about an endpoint and keeps in the
// declaration field of its record: what the adapter said of it and its
// declaration. The field is one compact JSON object with a fixed member order;
// a text that is not exactly what encodeMeta writes for the value it decodes to
// is not read, so one value has one text.
type endpointMeta struct {
	DisplayName string
	Description string
	Declaration authz.Declaration
}

type metaWire struct {
	DisplayName          string   `json:"display_name,omitempty"`
	Description          string   `json:"description,omitempty"`
	Paused               bool     `json:"paused,omitempty"`
	RefuseMultiRecipient bool     `json:"refuse_multi_recipient,omitempty"`
	RestrictSenders      bool     `json:"restrict_senders,omitempty"`
	AcceptFrom           []string `json:"accept_from,omitempty"`
	HiddenFromDirectory  bool     `json:"hidden_from_directory,omitempty"`
}

// checkAcceptFrom judges the list of senders of a declaration: at most
// MaxAcceptFrom, each a real address, none twice.
func checkAcceptFrom(list []ident.Address) error {
	if len(list) > MaxAcceptFrom {
		return fmt.Errorf("%d senders, at most %d", len(list), MaxAcceptFrom)
	}
	seen := make(map[ident.Address]bool, len(list))
	for i, a := range list {
		switch {
		case a.IsZero():
			return fmt.Errorf("sender %d is not an address", i)
		case seen[a]:
			return fmt.Errorf("sender %d is listed twice", i)
		}
		seen[a] = true
	}
	return nil
}

// encodeMeta is the text of the value. It refuses what decodeMeta would.
func encodeMeta(m endpointMeta) ([]byte, error) {
	d := m.Declaration
	if err := checkAcceptFrom(d.AcceptFrom); err != nil {
		return nil, fmt.Errorf("engine: declaration: %w", err)
	}
	w := metaWire{
		DisplayName: m.DisplayName, Description: m.Description,
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

// decodeMeta reads the text of a value. Nothing at all declares nothing.
func decodeMeta(b []byte) (endpointMeta, error) {
	if len(b) == 0 {
		return endpointMeta{}, nil
	}
	var w metaWire
	if err := strictjson.Decode(b, &w); err != nil {
		return endpointMeta{}, fmt.Errorf("engine: declaration: %w", err)
	}
	m := endpointMeta{DisplayName: w.DisplayName, Description: w.Description}
	m.Declaration = authz.Declaration{
		Paused: w.Paused, RefuseMultiRecipient: w.RefuseMultiRecipient,
		RestrictSenders: w.RestrictSenders, HiddenFromDirectory: w.HiddenFromDirectory,
	}
	for _, s := range w.AcceptFrom {
		a, err := ident.ParseAddress(s)
		if err != nil {
			return endpointMeta{}, fmt.Errorf("engine: declaration: accept_from: %w", err)
		}
		m.Declaration.AcceptFrom = append(m.Declaration.AcceptFrom, a)
	}
	again, err := encodeMeta(m)
	if err != nil {
		return endpointMeta{}, err
	}
	if !bytes.Equal(again, b) {
		return endpointMeta{}, errors.New("engine: declaration: not the canonical text of its value")
	}
	return m, nil
}
