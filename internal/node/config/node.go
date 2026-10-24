package config

import (
	"fmt"
	"math"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
)

// The settings that no other package decides. Each has its reason in the
// README: the numbers that another package owns are read from it, not repeated.
const (
	// DefaultNodeQueueLimit is how many unfinished deliveries the node holds.
	DefaultNodeQueueLimit = 10000
	// DefaultDiskReserveBytes is the free space below which new work is refused.
	DefaultDiskReserveBytes = 256 << 20
	// DefaultLogTotalBytes is the space all logs together may take.
	DefaultLogTotalBytes = 150 << 20
	// DefaultArchiveRetentionDays is how long the archive keeps its summaries.
	DefaultArchiveRetentionDays = 30
	// OpenDeliveryDiskGrowth is what an unfinished delivery adds to the disk
	// before it is finished and archived: its summary, its events and the
	// feedback it may give rise to. The disk reserve must cover this for as many
	// deliveries as the queue holds, plus the logs.
	OpenDeliveryDiskGrowth = 4 << 10
	// MaxArchiveRetentionDays is the most days of retention a time.Duration can
	// hold. A larger number would wrap around to a negative or short retention
	// in ArchiveRetention, and the archive would then discard its summaries at
	// once. The limit is almost three centuries, which no owner needs.
	MaxArchiveRetentionDays = math.MaxInt64 / int64(24*time.Hour)
)

// DefaultNodeSettings are the settings `init` writes. The limits another
// package decided come from that package.
func DefaultNodeSettings() NodeSettings {
	return NodeSettings{
		ReplyPermission:      true,
		NodeQueueLimit:       DefaultNodeQueueLimit,
		DiskReserveBytes:     DefaultDiskReserveBytes,
		NodePromptTurns:      handoff.DefaultMaxConcurrentTurns,
		ArchiveRetentionDays: DefaultArchiveRetentionDays,
		LogTotalBytes:        DefaultLogTotalBytes,
		EndpointDefaults: EndpointSettings{
			MaxQueue:     delivery.DefaultEndpointMaxQueue,
			BacklogLimit: delivery.DefaultEndpointBacklogLimit,
		},
	}
}

// Validate checks the settings of an endpoint with the rules of the package
// that owns them: delivery decides what a queue time and a backlog limit may be.
func (s EndpointSettings) Validate() error {
	if err := delivery.ValidateEndpointMaxQueue(s.MaxQueue); err != nil {
		return invalid("max_queue_s", err)
	}
	if err := delivery.ValidateEndpointBacklogLimit(s.BacklogLimit); err != nil {
		return invalid("backlog_limit", err)
	}
	return nil
}

// Validate checks the settings an owner gave one endpoint. A field that is not
// set is not checked: the node default applies.
func (o EndpointOverride) Validate() error {
	if o == (EndpointOverride{}) {
		return invalidf("", "no setting is given")
	}
	if o.MaxQueue != 0 {
		if err := delivery.ValidateEndpointMaxQueue(o.MaxQueue); err != nil {
			return invalid("max_queue_s", err)
		}
	}
	if o.BacklogLimit != 0 {
		if err := delivery.ValidateEndpointBacklogLimit(o.BacklogLimit); err != nil {
			return invalid("backlog_limit", err)
		}
	}
	return nil
}

// ArchiveRetention is how long the archive keeps its summaries, the one
// conversion of archive_retention_days to a duration. Validate refuses a number
// of days that the conversion would wrap around.
func (s NodeSettings) ArchiveRetention() time.Duration {
	return time.Duration(s.ArchiveRetentionDays) * 24 * time.Hour
}

func outOfRange(field string, v any) error {
	return invalid(field, fmt.Errorf("%w: %v (at least 1)", ErrOutOfRange, v))
}

// Validate checks every setting: the ones that another package owns by asking
// it, the others by the ranges the README gives, and the disk reserve against
// what the queue and the logs can fill.
func (s NodeSettings) Validate() error {
	if s.NodeQueueLimit < 1 {
		return outOfRange("node_queue_limit", s.NodeQueueLimit)
	}
	if s.DiskReserveBytes < 1 {
		return outOfRange("disk_reserve_bytes", s.DiskReserveBytes)
	}
	prompts := handoff.DefaultPromptParams()
	prompts.MaxConcurrentTurns = s.NodePromptTurns
	if err := prompts.Validate(); err != nil {
		return invalid("node_prompt_turns", err)
	}
	if s.ArchiveRetentionDays < 1 {
		return outOfRange("archive_retention_days", s.ArchiveRetentionDays)
	}
	if int64(s.ArchiveRetentionDays) > MaxArchiveRetentionDays {
		return invalid("archive_retention_days", fmt.Errorf("%w: %d (at most %d, the days a duration can hold)",
			ErrOutOfRange, s.ArchiveRetentionDays, MaxArchiveRetentionDays))
	}
	if s.LogTotalBytes < 1 {
		return outOfRange("log_total_bytes", s.LogTotalBytes)
	}
	if err := under("endpoint_defaults", s.EndpointDefaults.Validate()); err != nil {
		return err
	}
	if !diskReserveCovers(s.DiskReserveBytes, s.LogTotalBytes, s.NodeQueueLimit) {
		return invalid("disk_reserve_bytes", fmt.Errorf("%w: %d bytes do not cover %d bytes of logs and %d bytes for each of %d deliveries",
			ErrDiskReserveTooLow, s.DiskReserveBytes, s.LogTotalBytes, OpenDeliveryDiskGrowth, s.NodeQueueLimit))
	}
	return nil
}

// diskReserveCovers reports whether logTotal + OpenDeliveryDiskGrowth * queue
// fits in reserve, computed so that it cannot overflow. queue and logTotal are
// positive and reserve is not below logTotal when it is checked further.
func diskReserveCovers(reserve, logTotal int64, queue int) bool {
	if logTotal > reserve {
		return false
	}
	room := uint64(reserve - logTotal)
	return uint64(queue) <= room/OpenDeliveryDiskGrowth
}

// nodeWire is node.json.
type nodeWire struct {
	Format        int              `json:"format"`
	Version       uint64           `json:"version"`
	Settings      nodeSettingsWire `json:"settings"`
	LastOperation *operationWire   `json:"last_operation,omitempty"`
}

type nodeSettingsWire struct {
	ReplyPermission      bool                 `json:"reply_permission"`
	NodeQueueLimit       int                  `json:"node_queue_limit"`
	DiskReserveBytes     int64                `json:"disk_reserve_bytes"`
	NodePromptTurns      int                  `json:"node_prompt_turns"`
	ArchiveRetentionDays int                  `json:"archive_retention_days"`
	LogTotalBytes        int64                `json:"log_total_bytes"`
	EndpointDefaults     endpointDefaultsWire `json:"endpoint_defaults"`
}

type endpointDefaultsWire struct {
	MaxQueueS    int64 `json:"max_queue_s"`
	BacklogLimit int   `json:"backlog_limit"`
}

func decodeNode(data []byte) (Node, error) {
	var w nodeWire
	if err := decodeStrict(data, &w); err != nil {
		return Node{}, err
	}
	if err := checkHeader(w.Format, w.Version, true); err != nil {
		return Node{}, err
	}
	n := Node{
		Version: versionOf(w.Version),
		Settings: NodeSettings{
			ReplyPermission:      w.Settings.ReplyPermission,
			NodeQueueLimit:       w.Settings.NodeQueueLimit,
			DiskReserveBytes:     w.Settings.DiskReserveBytes,
			NodePromptTurns:      w.Settings.NodePromptTurns,
			ArchiveRetentionDays: w.Settings.ArchiveRetentionDays,
			LogTotalBytes:        w.Settings.LogTotalBytes,
			EndpointDefaults: EndpointSettings{
				MaxQueue:     secondsToDuration(w.Settings.EndpointDefaults.MaxQueueS),
				BacklogLimit: w.Settings.EndpointDefaults.BacklogLimit,
			},
		},
	}
	if err := under("settings", n.Settings.Validate()); err != nil {
		return Node{}, err
	}
	if w.LastOperation != nil {
		op, err := w.LastOperation.operation()
		if err != nil {
			return Node{}, err
		}
		n.LastOperation = op
	}
	return n, nil
}

// DecodeNode decodes node.json. The error is a *FileError that names the file.
func DecodeNode(data []byte) (Node, error) {
	n, err := decodeNode(data)
	if err != nil {
		return Node{}, fileErrorFor(NodePath, KindNode, err)
	}
	return n, nil
}

// Encode returns the bytes of node.json.
func (n Node) Encode() ([]byte, error) {
	maxQueue, err := durationSeconds("endpoint_defaults.max_queue_s", n.Settings.EndpointDefaults.MaxQueue)
	if err != nil {
		return nil, err
	}
	op, err := operationWireOf(n.LastOperation)
	if err != nil {
		return nil, err
	}
	s := n.Settings
	return encodeJSON(nodeWire{
		Format:  FormatVersion,
		Version: uint64(n.Version),
		Settings: nodeSettingsWire{
			ReplyPermission:      s.ReplyPermission,
			NodeQueueLimit:       s.NodeQueueLimit,
			DiskReserveBytes:     s.DiskReserveBytes,
			NodePromptTurns:      s.NodePromptTurns,
			ArchiveRetentionDays: s.ArchiveRetentionDays,
			LogTotalBytes:        s.LogTotalBytes,
			EndpointDefaults:     endpointDefaultsWire{MaxQueueS: maxQueue, BacklogLimit: s.EndpointDefaults.BacklogLimit},
		},
		LastOperation: op,
	})
}

// EndpointPatch names the endpoint settings a request changes; a nil field is
// not named.
type EndpointPatch struct {
	MaxQueue     *time.Duration
	BacklogLimit *int
}

// NodePatch names the node settings a request changes; a nil field is not
// named.
type NodePatch struct {
	ReplyPermission      *bool
	NodeQueueLimit       *int
	DiskReserveBytes     *int64
	NodePromptTurns      *int
	ArchiveRetentionDays *int
	LogTotalBytes        *int64
	EndpointDefaults     *EndpointPatch
}

type endpointPatchWire struct {
	MaxQueueS    *int64 `json:"max_queue_s,omitempty"`
	BacklogLimit *int   `json:"backlog_limit,omitempty"`
}

type nodePatchWire struct {
	ReplyPermission      *bool              `json:"reply_permission,omitempty"`
	NodeQueueLimit       *int               `json:"node_queue_limit,omitempty"`
	DiskReserveBytes     *int64             `json:"disk_reserve_bytes,omitempty"`
	NodePromptTurns      *int               `json:"node_prompt_turns,omitempty"`
	ArchiveRetentionDays *int               `json:"archive_retention_days,omitempty"`
	LogTotalBytes        *int64             `json:"log_total_bytes,omitempty"`
	EndpointDefaults     *endpointPatchWire `json:"endpoint_defaults,omitempty"`
}

func (w *endpointPatchWire) patch() EndpointPatch {
	var p EndpointPatch
	if w.MaxQueueS != nil {
		d := secondsToDuration(*w.MaxQueueS)
		p.MaxQueue = &d
	}
	p.BacklogLimit = w.BacklogLimit
	return p
}

// DecodeNodePatch decodes the settings object of a node.set request.
func DecodeNodePatch(data []byte) (NodePatch, error) {
	var w nodePatchWire
	if err := decodeStrict(data, &w); err != nil {
		return NodePatch{}, err
	}
	p := NodePatch{
		ReplyPermission:      w.ReplyPermission,
		NodeQueueLimit:       w.NodeQueueLimit,
		DiskReserveBytes:     w.DiskReserveBytes,
		NodePromptTurns:      w.NodePromptTurns,
		ArchiveRetentionDays: w.ArchiveRetentionDays,
		LogTotalBytes:        w.LogTotalBytes,
	}
	if w.EndpointDefaults != nil {
		e := w.EndpointDefaults.patch()
		p.EndpointDefaults = &e
	}
	return p, nil
}

// DecodeEndpointPatch decodes the settings object of an endpoints.set request.
func DecodeEndpointPatch(data []byte) (EndpointPatch, error) {
	var w endpointPatchWire
	if err := decodeStrict(data, &w); err != nil {
		return EndpointPatch{}, err
	}
	return w.patch(), nil
}

// isEmpty reports whether the patch names no setting.
func (p EndpointPatch) isEmpty() bool { return p.MaxQueue == nil && p.BacklogLimit == nil }

// isEmpty reports whether the patch names no setting.
func (p NodePatch) isEmpty() bool {
	return p.ReplyPermission == nil && p.NodeQueueLimit == nil && p.DiskReserveBytes == nil &&
		p.NodePromptTurns == nil && p.ArchiveRetentionDays == nil && p.LogTotalBytes == nil &&
		(p.EndpointDefaults == nil || p.EndpointDefaults.isEmpty())
}

// validate checks the values the patch names, each with the rule of its owner,
// before they are merged: a patch value of zero is a value, not an absence.
func (p EndpointPatch) validate() error {
	if p.MaxQueue != nil {
		if err := delivery.ValidateEndpointMaxQueue(*p.MaxQueue); err != nil {
			return invalid("max_queue_s", err)
		}
	}
	if p.BacklogLimit != nil {
		if err := delivery.ValidateEndpointBacklogLimit(*p.BacklogLimit); err != nil {
			return invalid("backlog_limit", err)
		}
	}
	return nil
}

// apply returns the settings with the patch's values in place of theirs.
func (p NodePatch) apply(s NodeSettings) NodeSettings {
	if p.ReplyPermission != nil {
		s.ReplyPermission = *p.ReplyPermission
	}
	if p.NodeQueueLimit != nil {
		s.NodeQueueLimit = *p.NodeQueueLimit
	}
	if p.DiskReserveBytes != nil {
		s.DiskReserveBytes = *p.DiskReserveBytes
	}
	if p.NodePromptTurns != nil {
		s.NodePromptTurns = *p.NodePromptTurns
	}
	if p.ArchiveRetentionDays != nil {
		s.ArchiveRetentionDays = *p.ArchiveRetentionDays
	}
	if p.LogTotalBytes != nil {
		s.LogTotalBytes = *p.LogTotalBytes
	}
	if e := p.EndpointDefaults; e != nil {
		if e.MaxQueue != nil {
			s.EndpointDefaults.MaxQueue = *e.MaxQueue
		}
		if e.BacklogLimit != nil {
			s.EndpointDefaults.BacklogLimit = *e.BacklogLimit
		}
	}
	return s
}

// apply returns the override with the patch's values in place of theirs.
func (p EndpointPatch) apply(o EndpointOverride) EndpointOverride {
	if p.MaxQueue != nil {
		o.MaxQueue = *p.MaxQueue
	}
	if p.BacklogLimit != nil {
		o.BacklogLimit = *p.BacklogLimit
	}
	return o
}
