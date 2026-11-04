package nodeproto

import (
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// DirectoryRequest asks for a page of the open endpoints the target shows the
// asker, after the opaque cursor of a previous page. A zero Limit leaves the
// page size to the target; a limit that is present is 1 to MaxDirectoryPage.
// The asking node is the one the connection authenticated.
type DirectoryRequest struct {
	Sender ident.EndpointID
	Cursor string
	Limit  int
}

type directoryRequestWire struct {
	Sender string `cbor:"1,keyasint"`
	Cursor string `cbor:"2,keyasint,omitempty"`
	Limit  uint64 `cbor:"3,keyasint,omitempty"`
}

// Encode returns the canonical bytes of the request. It refuses what
// DecodeDirectoryRequest would, a negative limit included.
func (r DirectoryRequest) Encode() ([]byte, error) {
	if r.Limit < 0 {
		return nil, invalid("limit %d", r.Limit)
	}
	return encodeChecked(directoryRequestWire{Sender: r.Sender.String(), Cursor: r.Cursor, Limit: uint64(r.Limit)}, DecodeDirectoryRequest)
}

// DecodeDirectoryRequest parses a directory request strictly, within the request
// limit of the protocol. A cursor is 1 to MaxCursorLen bytes of visible ASCII
// when present; its meaning is the target's.
func DecodeDirectoryRequest(data []byte) (DirectoryRequest, error) {
	var w directoryRequestWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolDirectory).Request); err != nil {
		return DirectoryRequest{}, err
	}
	sender, err := parseEndpointText(w.Sender, "sender")
	if err != nil {
		return DirectoryRequest{}, err
	}
	if err := checkCursor(w.Cursor); err != nil {
		return DirectoryRequest{}, err
	}
	if w.Limit > MaxDirectoryPage {
		return DirectoryRequest{}, invalid("limit %d, at most %d", w.Limit, MaxDirectoryPage)
	}
	return DirectoryRequest{Sender: sender, Cursor: w.Cursor, Limit: int(w.Limit)}, nil
}

func checkCursor(c string) error {
	if len(c) > MaxCursorLen {
		return invalid("cursor is %d bytes, at most %d", len(c), MaxCursorLen)
	}
	for i := 0; i < len(c); i++ {
		if c[i] < 0x21 || c[i] > 0x7e {
			return invalid("cursor has the byte %#x at %d", c[i], i)
		}
	}
	return nil
}

// DirectoryEntry is one open endpoint the target shows. It carries the
// endpoint's identifier and not its node: the asker knows the node it called
// and fills it in, and never takes one from the answer.
type DirectoryEntry struct {
	Endpoint    ident.EndpointID
	Kind        adapterreg.EndpointKind
	DisplayName string
	Description string
	Actions     []authz.Action
}

type directoryEntryWire struct {
	Endpoint    []byte   `cbor:"1,keyasint"`
	Kind        string   `cbor:"2,keyasint"`
	DisplayName string   `cbor:"3,keyasint"`
	Description string   `cbor:"4,keyasint"`
	Actions     []string `cbor:"5,keyasint"`
}

// DirectoryAnswer is the target's answer to a directory request. With the outcome
// ok it holds a page of entries and, when more follow, the cursor of the next
// page. A refused answer carries the reason name and a failed one nothing.
type DirectoryAnswer struct {
	Outcome    Outcome
	Entries    []DirectoryEntry
	NextCursor string
	Reason     string
}

type directoryAnswerWire struct {
	Outcome    uint64               `cbor:"1,keyasint"`
	Entries    []directoryEntryWire `cbor:"2,keyasint"`
	NextCursor string               `cbor:"3,keyasint,omitempty"`
	Reason     string               `cbor:"4,keyasint,omitempty"`
}

// Encode returns the canonical bytes of the answer. It refuses what
// DecodeDirectoryAnswer would.
func (a DirectoryAnswer) Encode() ([]byte, error) {
	entries := make([]directoryEntryWire, len(a.Entries))
	for i, e := range a.Entries {
		actions := make([]string, len(e.Actions))
		for j, act := range e.Actions {
			actions[j] = act.String()
		}
		entries[i] = directoryEntryWire{
			Endpoint: e.Endpoint.Bytes(), Kind: e.Kind.String(),
			DisplayName: e.DisplayName, Description: e.Description, Actions: actions,
		}
	}
	return encodeChecked(directoryAnswerWire{Outcome: uint64(a.Outcome), Entries: entries, NextCursor: a.NextCursor, Reason: a.Reason}, DecodeDirectoryAnswer)
}

// DecodeDirectoryAnswer parses a directory answer strictly, within the answer
// limit of the protocol. An entry names a kind and actions by their names, a
// display name of at most adapterproto.MaxDisplayName characters and a
// description of at most adapterproto.MaxDescription; the text is valid UTF-8,
// which the decoder guarantees.
func DecodeDirectoryAnswer(data []byte) (DirectoryAnswer, error) {
	var w directoryAnswerWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolDirectory).Answer); err != nil {
		return DirectoryAnswer{}, err
	}
	o, err := checkOutcome(w.Outcome, w.Reason)
	if err != nil {
		return DirectoryAnswer{}, err
	}
	if o != OutcomeOK && (len(w.Entries) > 0 || w.NextCursor != "") {
		return DirectoryAnswer{}, invalid("a %s answer carries no page", o)
	}
	if len(w.Entries) > MaxDirectoryPage {
		return DirectoryAnswer{}, invalid("%d entries, at most %d", len(w.Entries), MaxDirectoryPage)
	}
	if err := checkCursor(w.NextCursor); err != nil {
		return DirectoryAnswer{}, err
	}
	entries := make([]DirectoryEntry, len(w.Entries))
	for i, we := range w.Entries {
		e, err := entryFromWire(we)
		if err != nil {
			return DirectoryAnswer{}, invalid("entry %d: %v", i, err)
		}
		entries[i] = e
	}
	return DirectoryAnswer{Outcome: o, Entries: entries, NextCursor: w.NextCursor, Reason: w.Reason}, nil
}

func entryFromWire(w directoryEntryWire) (DirectoryEntry, error) {
	id, err := ident.EndpointIDFromBytes(w.Endpoint)
	if err != nil {
		return DirectoryEntry{}, err
	}
	kind, ok := kindByName(w.Kind)
	if !ok {
		return DirectoryEntry{}, invalid("kind %q", w.Kind)
	}
	if err := adapterproto.ValidateOptionalDisplayName(w.DisplayName); err != nil {
		return DirectoryEntry{}, invalid("display name: %v", err)
	}
	if n := utf8.RuneCountInString(w.Description); n > adapterproto.MaxDescription {
		return DirectoryEntry{}, invalid("description of %d characters, at most %d", n, adapterproto.MaxDescription)
	}
	actions := make([]authz.Action, len(w.Actions))
	seen := make(map[authz.Action]struct{}, len(w.Actions))
	for i, name := range w.Actions {
		a, ok := actionByName(name)
		if !ok {
			return DirectoryEntry{}, invalid("action %q", name)
		}
		if _, dup := seen[a]; dup {
			return DirectoryEntry{}, invalid("action %q is listed twice", name)
		}
		seen[a] = struct{}{}
		actions[i] = a
	}
	return DirectoryEntry{Endpoint: id, Kind: kind, DisplayName: w.DisplayName, Description: w.Description, Actions: actions}, nil
}

func kindByName(name string) (adapterreg.EndpointKind, bool) {
	for _, k := range adapterreg.Kinds() {
		if k.String() == name {
			return k, true
		}
	}
	return 0, false
}

func actionByName(name string) (authz.Action, bool) {
	for _, a := range authz.Actions {
		if a.String() == name {
			return a, true
		}
	}
	return 0, false
}
