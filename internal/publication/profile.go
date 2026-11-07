package publication

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// NodeProfileType is the type string of a node profile.
const NodeProfileType = "sidera/node-profile/1"

// Limits of a node profile's text, in characters unless stated. The name, the
// host name and the system follow the display-name rule of adapterproto
// (adapterproto.MaxDisplayName characters).
const (
	// MaxProfileLabelLen is the most characters of the system build and of the
	// Sidera version.
	MaxProfileLabelLen = 64
	// MaxPlatformLen is the most bytes of the platform, "<goos>/<goarch>".
	MaxPlatformLen = 32
)

// NodeProfile is the payload of a node profile: what a node says about the
// machine it runs on and its own name, for people and agents to tell nodes
// apart. It is descriptive: nothing in it takes part in addressing, trust or
// policy. Every text field is optional, and a field that is absent is the empty
// string.
type NodeProfile struct {
	// Node is the node the profile is about. It is signed by that node's network
	// key.
	Node ident.NodeID
	// IssuedAt is when the node made the profile, in whole seconds. Of two
	// profiles of one node the later one is the current one
	// (ProfileReplaces).
	IssuedAt time.Time
	// Name is what people call the node: the name its owner gave it, or the host
	// name.
	Name string
	// HostName is the first label of the machine's host name.
	HostName string
	// Platform is "<goos>/<goarch>", for example "linux/amd64".
	Platform string
	// System is the operating system and its release, for example
	// "Ubuntu 24.04.1 LTS".
	System string
	// SystemBuild is the build or kernel version of the system, for example
	// "6.8.0-45-generic".
	SystemBuild string
	// SideraVersion is the version of Sidera the node runs.
	SideraVersion string
}

// profileField is one optional text field of a profile: its name in the
// payload description (also what NewNodeProfile reports when it drops the
// field), where it lives in a NodeProfile and the rule it must satisfy. It is
// the one table of the rules, read by decoding and by NewNodeProfile.
type profileField struct {
	name  string
	of    func(*NodeProfile) *string
	check func(string) error
}

// profileFields lists the text fields in the order of their payload keys.
var profileFields = [...]profileField{
	{"name", func(p *NodeProfile) *string { return &p.Name }, checkDisplayText},
	{"host_name", func(p *NodeProfile) *string { return &p.HostName }, checkDisplayText},
	{"platform", func(p *NodeProfile) *string { return &p.Platform }, checkPlatform},
	{"system", func(p *NodeProfile) *string { return &p.System }, checkDisplayText},
	{"system_build", func(p *NodeProfile) *string { return &p.SystemBuild }, checkLabel},
	{"sidera_version", func(p *NodeProfile) *string { return &p.SideraVersion }, checkLabel},
}

// checkDisplayText applies the one display-name rule. The decoder refuses text
// that is not UTF-8 before this runs; the check is repeated so that
// NewNodeProfile, which gets text from the machine, applies the same rule.
func checkDisplayText(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("not valid UTF-8")
	}
	return adapterproto.ValidateDisplayName(s)
}

// checkLabel is the display-name rule with the shorter limit of the system
// build and the Sidera version.
func checkLabel(s string) error {
	if err := checkDisplayText(s); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(s); n > MaxProfileLabelLen {
		return fmt.Errorf("%d characters, at most %d", n, MaxProfileLabelLen)
	}
	return nil
}

// checkPlatform accepts "<goos>/<goarch>": two non-empty parts of lower-case
// ASCII letters and digits, at most MaxPlatformLen bytes in all.
func checkPlatform(s string) error {
	if len(s) > MaxPlatformLen {
		return fmt.Errorf("%d bytes, at most %d", len(s), MaxPlatformLen)
	}
	goos, goarch, ok := strings.Cut(s, "/")
	if !ok || !isPlatformWord(goos) || !isPlatformWord(goarch) {
		return errors.New("not of the form <goos>/<goarch>")
	}
	return nil
}

func isPlatformWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// profileWire is the payload: integer keys, every text field left out when
// absent, so one profile has one encoding.
type profileWire struct {
	Node          []byte `cbor:"1,keyasint"`
	IssuedAt      int64  `cbor:"2,keyasint"`
	Name          string `cbor:"3,keyasint,omitempty"`
	HostName      string `cbor:"4,keyasint,omitempty"`
	Platform      string `cbor:"5,keyasint,omitempty"`
	System        string `cbor:"6,keyasint,omitempty"`
	SystemBuild   string `cbor:"7,keyasint,omitempty"`
	SideraVersion string `cbor:"8,keyasint,omitempty"`
}

func profileToWire(p NodeProfile) (profileWire, error) {
	issued, err := record.TimeToWire(p.IssuedAt)
	if err != nil {
		return profileWire{}, fmt.Errorf("issued_at: %w", err)
	}
	return profileWire{
		Node: p.Node.Bytes(), IssuedAt: issued,
		Name: p.Name, HostName: p.HostName, Platform: p.Platform,
		System: p.System, SystemBuild: p.SystemBuild, SideraVersion: p.SideraVersion,
	}, nil
}

func profileFromWire(w profileWire) (NodeProfile, error) {
	node, err := ident.NodeIDFromBytes(w.Node)
	if err != nil {
		return NodeProfile{}, fmt.Errorf("node: %w", err)
	}
	issued, err := record.TimeFromWire(w.IssuedAt)
	if err != nil {
		return NodeProfile{}, fmt.Errorf("issued_at: %w", err)
	}
	p := NodeProfile{
		Node: node, IssuedAt: issued,
		Name: w.Name, HostName: w.HostName, Platform: w.Platform,
		System: w.System, SystemBuild: w.SystemBuild, SideraVersion: w.SideraVersion,
	}
	for _, f := range profileFields {
		// An empty string is an absent field: the strict decoder has already
		// refused one written out, so it can only be absent here.
		if s := *f.of(&p); s != "" {
			if err := f.check(s); err != nil {
				return NodeProfile{}, fmt.Errorf("%w: %s: %w", ErrProfile, f.name, err)
			}
		}
	}
	return p, nil
}

// NodeProfileDescriptor describes the node profile record. It is signed by one
// key: the network key that accepted names for the profile's node
// (record.NetworkKeyOf), at now. The profile is separate from the declaration
// because it only describes: it changes rarely and no policy decision reads it.
func NodeProfileDescriptor(accepted record.AcceptedNetworkKey, now time.Time) record.Descriptor[NodeProfile] {
	return record.NewDescriptor(NodeProfileType,
		profileToWire,
		profileFromWire,
		func(p NodeProfile) []record.SignerSlot {
			return []record.SignerSlot{record.NetworkKeyOf(p.Node, accepted, now)}
		},
	)
}

// NodeProfileNode returns the node a profile claims to be about, read before
// the signature is checked, only so that the verifier can look up the accepted
// network-key certificate for that node and build the NodeProfileDescriptor.
// The result is untrusted until record.Verify accepts the record.
func NodeProfileNode(r record.Record) (ident.NodeID, error) {
	u, err := record.Peek(NodeProfileDescriptor(record.AcceptedNetworkKey{}, time.Time{}), r)
	if err != nil {
		return ident.NodeID{}, err
	}
	return u.Payload().Node, nil
}

// NewNodeProfile makes a publishable profile out of values read from the
// machine, which can be anything. The node and the time are required and an
// invalid one is an error. A text field that is empty or does not satisfy its
// rule is left out instead of failing the profile, so that a machine that
// reports an odd host name still publishes the rest. The names of the fields
// that were present but invalid are returned, in the order of the payload keys,
// for the caller to log; a field that was simply empty is not reported. The time
// is cut to whole seconds, as on the wire.
func NewNodeProfile(raw NodeProfile) (NodeProfile, []string, error) {
	if raw.Node.IsZero() {
		return NodeProfile{}, nil, fmt.Errorf("node: %w", ident.ErrZero)
	}
	sec, err := record.TimeToWire(raw.IssuedAt)
	if err != nil {
		return NodeProfile{}, nil, fmt.Errorf("issued_at: %w", err)
	}
	if raw.IssuedAt, err = record.TimeFromWire(sec); err != nil {
		return NodeProfile{}, nil, fmt.Errorf("issued_at: %w", err)
	}
	var dropped []string
	for _, f := range profileFields {
		s := f.of(&raw)
		if *s != "" && f.check(*s) != nil {
			*s = ""
			dropped = append(dropped, f.name)
		}
	}
	return raw, dropped, nil
}

// ProfileReplaces says whether incoming should replace stored as the profile
// kept for a node: it was issued strictly later. Equal times keep what is
// stored, whether the record is the same or not, so two profiles from one
// second are never swapped back and forth by the order they arrive in; another
// node's profile never replaces. The rule uses the issue time and not a
// monotonic counter on purpose: a profile is only descriptive, so a clock set
// back can at worst delay a rename, and the counters that detect a rollback of
// the node's other records stay untouched.
func ProfileReplaces(stored, incoming record.Verified[NodeProfile]) bool {
	s, in := stored.Payload(), incoming.Payload()
	return s.Node == in.Node && in.IssuedAt.After(s.IssuedAt)
}
