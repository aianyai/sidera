package publication

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// DeclarationType is the type string of a node declaration.
const DeclarationType = "sidera/node-declaration/1"

// Limits of a declaration. They bound what a hostile node can make others
// store and parse; an honest node stays far below them.
const (
	// BootNonceLen is the length of the boot nonce.
	BootNonceLen = 16
	// MaxEndpoints is the most endpoints one declaration lists.
	MaxEndpoints = 256
	// MaxNameLen is the longest endpoint display name, in bytes of UTF-8.
	MaxNameLen = 64
)

// BootNonce is chosen anew at every start of the node and published with the
// declaration, so a declaration written after a restart differs from every
// earlier one even when nothing else changed.
type BootNonce [BootNonceLen]byte

// EndpointKind says what an endpoint is.
type EndpointKind int

const (
	// EndpointKindInvalid is the zero value; it is never a valid kind.
	EndpointKindInvalid EndpointKind = iota
	// EndpointSession is an endpoint for one native agent session.
	EndpointSession
	// EndpointRuntime is an endpoint for a local runtime instance that accepts
	// control signals such as session creation.
	EndpointRuntime
)

// Wire values of the kinds, an independent table so that reordering the Go
// constants never changes what a signed declaration means.
const (
	wireKindSession = 1
	wireKindRuntime = 2
)

// Reception holds the reception preferences an endpoint publishes. The
// preferences only narrow what node policy allows. The list of senders an
// endpoint accepts is not published: it stays on the node, and only the fact
// that senders are restricted is.
type Reception struct {
	// Paused: the endpoint accepts no new messages for now.
	Paused bool
	// RefuseMultiRecipient: the endpoint rejects messages addressed to several
	// recipients.
	RefuseMultiRecipient bool
	// RestrictSenders: the endpoint accepts messages only from senders it lists
	// locally.
	RestrictSenders bool
	// HiddenFromDirectory: the endpoint is left out of directory listings. It
	// does not affect reachability.
	HiddenFromDirectory bool
}

// Wire bits of the reception preferences. An unknown bit is refused.
const (
	bitPaused               = 1 << 0
	bitRefuseMultiRecipient = 1 << 1
	bitRestrictSenders      = 1 << 2
	bitHiddenFromDirectory  = 1 << 3
	knownReceptionBits      = bitPaused | bitRefuseMultiRecipient | bitRestrictSenders | bitHiddenFromDirectory
)

func (r Reception) bits() uint64 {
	var v uint64
	for _, f := range []struct {
		on  bool
		bit uint64
	}{
		{r.Paused, bitPaused},
		{r.RefuseMultiRecipient, bitRefuseMultiRecipient},
		{r.RestrictSenders, bitRestrictSenders},
		{r.HiddenFromDirectory, bitHiddenFromDirectory},
	} {
		if f.on {
			v |= f.bit
		}
	}
	return v
}

func receptionFromBits(v uint64) (Reception, error) {
	if v&^knownReceptionBits != 0 {
		return Reception{}, fmt.Errorf("%w: unknown reception bits %#x", ErrEndpoint, v&^knownReceptionBits)
	}
	return Reception{
		Paused:               v&bitPaused != 0,
		RefuseMultiRecipient: v&bitRefuseMultiRecipient != 0,
		RestrictSenders:      v&bitRestrictSenders != 0,
		HiddenFromDirectory:  v&bitHiddenFromDirectory != 0,
	}, nil
}

// EndpointDeclaration describes one endpoint of the node.
type EndpointDeclaration struct {
	ID   ident.EndpointID
	Kind EndpointKind
	// Name is a display name. It is for people only: it never takes part in
	// addressing, authorization or deduplication. It may be empty and is at most
	// MaxNameLen bytes. Text that is not valid UTF-8 does not decode: the strict
	// decoder refuses it before any rule of this package is asked.
	Name      string
	Reception Reception
}

// Declaration is the payload of a node declaration: what a node says about
// itself. Reachability (addresses, relays) is a separate record.
type Declaration struct {
	// Node is the node the declaration is about. It is signed by that node's
	// network key.
	Node ident.NodeID
	// Counter is the node's declaration counter, from 1, raised with every new
	// declaration. Peers keep only the highest (DecideDeclaration).
	Counter uint64
	// BootNonce is chosen at every start of the node.
	BootNonce BootNonce
	// AcceptsInheritance is the node's switch for inherited trust. Other nodes
	// read it to decide whether inheritance holds between them and this node
	// (trust.View.DeclaredAcceptsInheritance).
	AcceptsInheritance bool
	// OffersRelay says the node is willing to act as a relay.
	OffersRelay bool
	// MaxQueue is the longest an endpoint of this node keeps a message queued. It
	// is a whole number of seconds, above zero and at most delivery.MaxQueueCap:
	// a declaration over the protocol limit is refused, because senders compute
	// their pull deadline from the limit and not from the declared value.
	MaxQueue time.Duration
	// LeaseRenewal is the longest the node lets one lease live in total. It is a
	// whole number of seconds, above zero and at most delivery.LeaseRenewalCap,
	// for the same reason.
	LeaseRenewal time.Duration
	// Endpoints are the node's endpoints. The wire form lists them in ascending
	// order of their identifiers, each once; encoding sorts them, so the order
	// given here does not matter and decoding returns them sorted.
	Endpoints []EndpointDeclaration
}

type endpointWire struct {
	ID        []byte `cbor:"1,keyasint"`
	Kind      uint64 `cbor:"2,keyasint"`
	Name      string `cbor:"3,keyasint,omitempty"`
	Reception uint64 `cbor:"4,keyasint,omitempty"`
}

type declarationWire struct {
	Node               []byte         `cbor:"1,keyasint"`
	Counter            uint64         `cbor:"2,keyasint"`
	BootNonce          []byte         `cbor:"3,keyasint"`
	AcceptsInheritance bool           `cbor:"4,keyasint"`
	OffersRelay        bool           `cbor:"5,keyasint"`
	MaxQueueSecs       uint64         `cbor:"6,keyasint"`
	LeaseRenewalSecs   uint64         `cbor:"7,keyasint"`
	Endpoints          []endpointWire `cbor:"8,keyasint"`
}

func kindToWire(k EndpointKind) (uint64, error) {
	switch k {
	case EndpointSession:
		return wireKindSession, nil
	case EndpointRuntime:
		return wireKindRuntime, nil
	}
	return 0, fmt.Errorf("%w: kind %d", ErrEndpoint, k)
}

func kindFromWire(v uint64) (EndpointKind, error) {
	switch v {
	case wireKindSession:
		return EndpointSession, nil
	case wireKindRuntime:
		return EndpointRuntime, nil
	}
	return EndpointKindInvalid, fmt.Errorf("%w: wire kind %d", ErrEndpoint, v)
}

// wholeSeconds converts a duration to wire seconds. A duration that is
// negative or not a whole number of seconds has no wire form.
func wholeSeconds(d time.Duration) (uint64, bool) {
	if d < 0 || d%time.Second != 0 {
		return 0, false
	}
	return uint64(d / time.Second), true
}

func declarationToWire(d Declaration) (declarationWire, error) {
	queue, ok := wholeSeconds(d.MaxQueue)
	if !ok {
		return declarationWire{}, fmt.Errorf("%w: %v", ErrQueueTime, d.MaxQueue)
	}
	lease, ok := wholeSeconds(d.LeaseRenewal)
	if !ok {
		return declarationWire{}, fmt.Errorf("%w: %v", ErrLeaseRenewal, d.LeaseRenewal)
	}
	// Sorted so that one declaration has one encoding whatever order the caller
	// listed its endpoints in.
	sorted := slices.Clone(d.Endpoints)
	slices.SortFunc(sorted, func(a, b EndpointDeclaration) int { return bytes.Compare(a.ID.Bytes(), b.ID.Bytes()) })
	eps := make([]endpointWire, len(sorted))
	for i, e := range sorted {
		kind, err := kindToWire(e.Kind)
		if err != nil {
			return declarationWire{}, err
		}
		eps[i] = endpointWire{ID: e.ID.Bytes(), Kind: kind, Name: e.Name, Reception: e.Reception.bits()}
	}
	return declarationWire{
		Node: d.Node.Bytes(), Counter: d.Counter, BootNonce: d.BootNonce[:],
		AcceptsInheritance: d.AcceptsInheritance, OffersRelay: d.OffersRelay,
		MaxQueueSecs: queue, LeaseRenewalSecs: lease, Endpoints: eps,
	}, nil
}

func declarationFromWire(w declarationWire) (Declaration, error) {
	node, err := ident.NodeIDFromBytes(w.Node)
	if err != nil {
		return Declaration{}, fmt.Errorf("node: %w", err)
	}
	if err := record.RequireCounter(w.Counter); err != nil {
		return Declaration{}, fmt.Errorf("counter: %w", err)
	}
	var nonce BootNonce
	if len(w.BootNonce) != BootNonceLen {
		return Declaration{}, fmt.Errorf("%w: %d bytes, want %d", ErrBootNonce, len(w.BootNonce), BootNonceLen)
	}
	copy(nonce[:], w.BootNonce)
	if nonce == (BootNonce{}) {
		return Declaration{}, fmt.Errorf("%w: all zero", ErrBootNonce)
	}
	queue := durationFromWire(w.MaxQueueSecs)
	if err := delivery.ValidateEndpointMaxQueue(queue); err != nil {
		return Declaration{}, fmt.Errorf("%w: %w", ErrQueueTime, err)
	}
	lease := durationFromWire(w.LeaseRenewalSecs)
	if lease <= 0 || lease > delivery.LeaseRenewalCap {
		return Declaration{}, fmt.Errorf("%w: %v, limit %v", ErrLeaseRenewal, lease, delivery.LeaseRenewalCap)
	}
	if len(w.Endpoints) > MaxEndpoints {
		return Declaration{}, fmt.Errorf("%w: %d endpoints, at most %d", ErrEndpoint, len(w.Endpoints), MaxEndpoints)
	}
	d := Declaration{
		Node: node, Counter: w.Counter, BootNonce: nonce,
		AcceptsInheritance: w.AcceptsInheritance, OffersRelay: w.OffersRelay,
		MaxQueue: queue, LeaseRenewal: lease,
	}
	var prev []byte
	for i, e := range w.Endpoints {
		id, err := ident.EndpointIDFromBytes(e.ID)
		if err != nil {
			return Declaration{}, fmt.Errorf("endpoint %d: %w", i, err)
		}
		if i > 0 && bytes.Compare(prev, e.ID) >= 0 {
			return Declaration{}, fmt.Errorf("%w: endpoint %d repeated or out of order", ErrEndpoint, i)
		}
		prev = e.ID
		kind, err := kindFromWire(e.Kind)
		if err != nil {
			return Declaration{}, fmt.Errorf("endpoint %d: %w", i, err)
		}
		if len(e.Name) > MaxNameLen {
			return Declaration{}, fmt.Errorf("%w: endpoint %d name is %d bytes, at most %d", ErrEndpoint, i, len(e.Name), MaxNameLen)
		}
		reception, err := receptionFromBits(e.Reception)
		if err != nil {
			return Declaration{}, fmt.Errorf("endpoint %d: %w", i, err)
		}
		d.Endpoints = append(d.Endpoints, EndpointDeclaration{ID: id, Kind: kind, Name: e.Name, Reception: reception})
	}
	return d, nil
}

// durationFromWire converts wire seconds to a duration. A value too large for
// a duration is returned as a negative one, which every limit check refuses.
func durationFromWire(secs uint64) time.Duration {
	if secs > uint64(math.MaxInt64/int64(time.Second)) {
		return -1
	}
	return time.Duration(secs) * time.Second
}

// DeclarationDescriptor describes the declaration record for a verification at
// now. It is signed by one key: the network key that accepted names for the
// declaration's node (record.NetworkKeyOf). A declaration signed by the node's
// identity key, or by the network key of an expired or revoked certificate, is
// refused, with the binding reason in the error.
func DeclarationDescriptor(accepted record.AcceptedNetworkKey, now time.Time) record.Descriptor[Declaration] {
	return record.NewDescriptor(DeclarationType,
		declarationToWire,
		declarationFromWire,
		func(d Declaration) []record.SignerSlot {
			return []record.SignerSlot{record.NetworkKeyOf(d.Node, accepted, now)}
		},
	)
}

// DeclaredInheritance returns the entry of trust.View.DeclaredAcceptsInheritance
// this declaration stands for. The caller uses the declaration that
// DecideDeclaration last accepted for the node.
func (d Declaration) DeclaredInheritance() (ident.NodeID, bool) {
	return d.Node, d.AcceptsInheritance
}
