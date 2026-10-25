package config

import (
	"encoding/json"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// FormatVersion is the format of every file of config/. It is not the version
// a management request names: it is what a future change of the format reads
// to know what it migrates.
const FormatVersion = 1

// Operation is the management operation that produced the current version of a
// file, with its result. It is stored in the file it changed, in the same
// replacement as the change, so the effect and its result cannot be told
// apart by a crash. A file the owner edited by hand has none.
type Operation struct {
	ID admin.OperationID
	// Person is who sent the request; the zero value is the local owner.
	Person      ident.PersonID
	Fingerprint admin.Fingerprint
	// Result is one compact JSON object.
	Result json.RawMessage
}

// Key is the key under which the management layer files the operation's
// result.
func (o Operation) Key() admin.RequestKey { return admin.RequestKey{Person: o.Person, ID: o.ID} }

// Request is what a versioned change takes from the management layer: which
// request it is, and the version it was based on.
type Request struct {
	// Key is the key the request's results are filed under, as admin.KeyFor
	// makes it from the actor and the operation id. The change records its
	// person and id in last_operation, so the replay lookup finds them under the
	// same key.
	Key         admin.RequestKey
	Fingerprint admin.Fingerprint
	// Expected is the version of the collection the sender last saw.
	Expected admin.Version
}

// ResultFunc builds the result that is stored with a change, from the state
// the change produced. The management layer owns the shape of a result; the
// version is known only once the change is made, so it is built here.
type ResultFunc[T any] func(after T) (json.RawMessage, error)

// AfterCommit is returned by a write function that runs inside a transaction
// of another package. The caller calls it after Commit returned nil, and not
// otherwise: only then does the in-memory configuration take the change over.
type AfterCommit func()

// OpenEndpoints says whether an endpoint is open now. The session endpoints
// that are closed are the ones whose settings a write of instances.json drops.
// A nil value drops nothing.
type OpenEndpoints func(ident.EndpointID) bool

// EndpointSettings are the settings of one endpoint that an owner can change.
type EndpointSettings struct {
	// MaxQueue is the longest the endpoint keeps a message queued.
	MaxQueue time.Duration
	// BacklogLimit is how many messages the endpoint holds queued.
	BacklogLimit int
}

// EndpointOverride is the settings an owner gave one endpoint. A zero field is
// not set: the node default applies.
type EndpointOverride struct {
	MaxQueue     time.Duration
	BacklogLimit int
}

// NodeSettings are the settings of the node.
type NodeSettings struct {
	// ReplyPermission is whether a reply with evidence may reach an endpoint
	// that did not ask for it.
	ReplyPermission bool
	// NodeQueueLimit is how many unfinished deliveries the node holds.
	NodeQueueLimit int
	// DiskReserveBytes is the free space below which new work is refused.
	DiskReserveBytes int64
	// NodePromptTurns is how many turns started by prompts may run at once.
	NodePromptTurns int
	// ArchiveRetentionDays is how long the archive keeps what it summarises.
	ArchiveRetentionDays int
	// LogTotalBytes is the space all logs together may take.
	LogTotalBytes int64
	// EndpointDefaults apply to an endpoint without settings of its own.
	EndpointDefaults EndpointSettings
}

// Node is the content of node.json.
type Node struct {
	Version       admin.Version
	Settings      NodeSettings
	LastOperation *Operation
}

// PolicyFile is the content of policy.json.
type PolicyFile struct {
	Version       admin.Version
	Policy        authz.Policy
	LastOperation *Operation
}

// Preset is one preset: what local sources may do with it, and the values it
// gives the adapters.
type Preset struct {
	// LocalSources is the preset's statement about local sources. It has no
	// default: a preset without one is not valid.
	LocalSources authz.PresetLocalAccess
	Description  string
	// Config holds, per adapter id, the values of the manifest's preset fields.
	// Only the shape is checked here; the manifest decides what is valid.
	Config map[string]map[string]json.RawMessage
}

// Presets is the content of presets.json.
type Presets struct {
	Version       admin.Version
	Items         map[string]Preset
	LastOperation *Operation
}

// Instance is one adapter instance.
type Instance struct {
	Adapter string
	Enabled bool
	// Config holds the values of the manifest's instance fields.
	Config map[string]json.RawMessage
	// Endpoints are the settings owners gave single endpoints.
	Endpoints map[ident.EndpointID]EndpointOverride
}

// Instances is the content of instances.json.
type Instances struct {
	Version       admin.Version
	Items         map[string]Instance
	LastOperation *Operation
}

// EndpointIDs are the endpoint ids of one instance that must outlive the
// run-time state: the runtime endpoint and the direct endpoints by key.
type EndpointIDs struct {
	Runtime ident.EndpointID
	Direct  map[string]ident.EndpointID
}

// IDChange is an endpoint id that was looked up or minted for an instance, and
// the whole set of ids that holds it.
type IDChange struct {
	Instance string
	ID       ident.EndpointID
	// New is true when the id was minted and has to be written.
	New  bool
	ids  EndpointIDs
	data []byte // the file of ids, encoded where the id was minted
}

// Backup is the owner's confirmation that the self-revocation was kept offline.
type Backup struct {
	ConfirmedAt time.Time
	// Digest is the digest of the self-revocation that was confirmed.
	Digest record.Digest
}

// Confirms reports whether the confirmation is for the given self-revocation:
// the confirmation of an earlier identity says nothing about this one.
func (b Backup) Confirms(selfRevocation record.Digest) bool { return b.Digest == selfRevocation }

// Adapter is an installed adapter package.
type Adapter struct {
	ID       string
	Version  string
	Manifest manifest.Manifest
}

// Staged is a package copied into a staging directory next to the versions,
// parsed there, and not installed yet.
type Staged struct {
	ID       string
	Version  string
	Dir      string
	Manifest manifest.Manifest
}

// GCReason says why an adapter directory entry is garbage.
type GCReason uint8

// What adapter-gc removes.
const (
	// GCNotInstalled: an adapter directory without a current pointer: an
	// installation that did not finish.
	GCNotInstalled GCReason = iota + 1
	// GCStaging: a staging directory.
	GCStaging
	// GCSuperseded: a version directory that the current pointer does not name.
	GCSuperseded
)

// String names the reason.
func (r GCReason) String() string {
	switch r {
	case GCNotInstalled:
		return "not_installed"
	case GCStaging:
		return "staging"
	case GCSuperseded:
		return "superseded"
	}
	return "unknown"
}

// GCItem is one directory adapter-gc removes.
type GCItem struct {
	Path   string
	Reason GCReason
}
