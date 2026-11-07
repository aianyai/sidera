package nodeproto

import (
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
// endpoint's identifier and not its node: the asker knows the node it called and
// fills it in, and never takes one from the answer.
type DirectoryEntry struct {
	Endpoint    ident.EndpointID
	Kind        adapterreg.EndpointKind
	DisplayName string
	Description string
	Actions     []authz.Action
	// Adapter says which adapter serves the endpoint; nil when the target does not
	// say. It exists only under ProtocolDirectory.
	Adapter *DirectoryAdapter
}

// DirectoryAdapter is the adapter instance behind a directory entry: the
// instance that serves the endpoint on its node, the adapter it runs and,
// when the target knows them, the adapter's version and the runtime it drives.
// An instance and an adapter id follow the syntax of an adapter identifier, a
// version is a Semantic Versioning 2.0.0 version, and the runtime follows the
// rules of the probe that reports it and, as a text shown to a person, those of
// a display name.
type DirectoryAdapter struct {
	Instance string
	ID       string
	Version  string
	Runtime  *adapterproto.RuntimeInfo
}

type directoryEntryWire struct {
	Endpoint    []byte                `cbor:"1,keyasint"`
	Kind        string                `cbor:"2,keyasint"`
	DisplayName string                `cbor:"3,keyasint"`
	Description string                `cbor:"4,keyasint"`
	Actions     []string              `cbor:"5,keyasint"`
	Adapter     *directoryAdapterWire `cbor:"6,keyasint,omitempty"`
}

type directoryAdapterWire struct {
	Instance string                `cbor:"1,keyasint"`
	ID       string                `cbor:"2,keyasint"`
	Version  string                `cbor:"3,keyasint,omitempty"`
	Runtime  *directoryRuntimeWire `cbor:"4,keyasint,omitempty"`
}

type directoryRuntimeWire struct {
	Name    string `cbor:"1,keyasint"`
	Version string `cbor:"2,keyasint"`
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

// carriesAdapter reports whether the entries of a directory protocol name their
// adapter, and refuses an identifier that is no directory protocol.
func carriesAdapter(protocol string) (bool, error) {
	switch protocol {
	case ProtocolDirectory:
		return true, nil
	case PreviousProtocolDirectory, LegacyProtocolDirectory:
		return false, nil
	}
	return false, invalid("%q is no directory protocol", protocol)
}

// Encode returns the canonical bytes of the answer under ProtocolDirectory. It
// refuses what DecodeDirectoryAnswer would.
func (a DirectoryAnswer) Encode() ([]byte, error) { return a.EncodeFor(ProtocolDirectory) }

// EncodeFor returns the canonical bytes of the answer as the directory protocol
// of that identifier carries it. The older protocols have no adapter in an entry,
// so the adapters of the entries are left out. It refuses what
// DecodeDirectoryAnswerFor would.
func (a DirectoryAnswer) EncodeFor(protocol string) ([]byte, error) {
	withAdapter, err := carriesAdapter(protocol)
	if err != nil {
		return nil, err
	}
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
		if withAdapter && e.Adapter != nil {
			entries[i].Adapter = e.Adapter.toWire()
		}
	}
	return encodeChecked(directoryAnswerWire{Outcome: uint64(a.Outcome), Entries: entries, NextCursor: a.NextCursor, Reason: a.Reason},
		func(b []byte) (DirectoryAnswer, error) { return DecodeDirectoryAnswerFor(protocol, b) })
}

func (d DirectoryAdapter) toWire() *directoryAdapterWire {
	w := &directoryAdapterWire{Instance: d.Instance, ID: d.ID, Version: d.Version}
	if d.Runtime != nil {
		w.Runtime = &directoryRuntimeWire{Name: d.Runtime.Name, Version: d.Runtime.Version}
	}
	return w
}

// DecodeDirectoryAnswer parses a directory answer of ProtocolDirectory.
func DecodeDirectoryAnswer(data []byte) (DirectoryAnswer, error) {
	return DecodeDirectoryAnswerFor(ProtocolDirectory, data)
}

// DecodeDirectoryAnswerFor parses a directory answer strictly, within the answer
// limit of the protocol, as the directory protocol of that identifier carries it:
// an entry of an older protocol has no adapter, and an answer that has one is
// invalid, as it is to a node that speaks only that protocol. An entry names a
// kind and actions by their names, a display name of at most
// adapterproto.MaxDisplayName characters holding none of the characters
// adapterproto.RefusedInPersonText names, a description of at most
// adapterproto.MaxDescription holding none that adapterproto.RefusedInDescription
// names, and, under ProtocolDirectory, an adapter that follows the rules of
// DirectoryAdapter. An entry that breaks a rule makes the whole answer invalid;
// the text is valid UTF-8, which the decoder guarantees.
func DecodeDirectoryAnswerFor(protocol string, data []byte) (DirectoryAnswer, error) {
	withAdapter, err := carriesAdapter(protocol)
	if err != nil {
		return DirectoryAnswer{}, err
	}
	var w directoryAnswerWire
	if err := record.UnmarshalStrict(data, &w, limit(protocol).Answer); err != nil {
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
		if we.Adapter != nil && !withAdapter {
			return DirectoryAnswer{}, invalid("entry %d: an adapter in %s", i, protocol)
		}
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
	if err := adapterproto.ValidateOptionalDescription(w.Description); err != nil {
		return DirectoryEntry{}, invalid("description: %v", err)
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
	adapter, err := adapterFromWire(w.Adapter)
	if err != nil {
		return DirectoryEntry{}, invalid("adapter: %v", err)
	}
	return DirectoryEntry{Endpoint: id, Kind: kind, DisplayName: w.DisplayName, Description: w.Description, Actions: actions, Adapter: adapter}, nil
}

// adapterFromWire reads the adapter of an entry, if it has one. The instance is
// named as the configuration names it, with the syntax of an adapter identifier.
func adapterFromWire(w *directoryAdapterWire) (*DirectoryAdapter, error) {
	if w == nil {
		return nil, nil
	}
	if err := adapterproto.ValidateAdapterID(w.Instance); err != nil {
		return nil, invalid("instance: %v", err)
	}
	if err := adapterproto.ValidateAdapterID(w.ID); err != nil {
		return nil, invalid("id: %v", err)
	}
	if w.Version != "" {
		if err := adapterproto.ValidateSemVer(w.Version); err != nil {
			return nil, invalid("version: %v", err)
		}
	}
	a := &DirectoryAdapter{Instance: w.Instance, ID: w.ID, Version: w.Version}
	if w.Runtime != nil {
		rt := adapterproto.RuntimeInfo{Name: w.Runtime.Name, Version: w.Runtime.Version}
		if err := rt.Validate(); err != nil {
			return nil, invalid("runtime: %v", err)
		}
		// The probe bounds the length of both; both are shown to a person, so they
		// are held to the character rule of a display name as well.
		if err := adapterproto.ValidatePersonText(rt.Name + rt.Version); err != nil {
			return nil, invalid("runtime: %v", err)
		}
		a.Runtime = &rt
	}
	return a, nil
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
