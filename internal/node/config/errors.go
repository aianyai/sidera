package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aianyai/sidera/internal/admin"
)

// FileKind says which kind of file or directory of config/ an error is about.
type FileKind uint8

// The kinds of file and directory the loader reads.
const (
	KindDir FileKind = iota + 1
	KindNode
	KindPolicy
	KindPresets
	KindInstances
	KindEndpointIDs
	KindBackup
	KindRevocation
	KindAdapterCurrent
	KindAdapterManifest
)

var kindNames = [...]string{
	KindDir:             "directory",
	KindNode:            "node settings",
	KindPolicy:          "policy",
	KindPresets:         "presets",
	KindInstances:       "instances",
	KindEndpointIDs:     "endpoint ids",
	KindBackup:          "revocation backup",
	KindRevocation:      "revocation record",
	KindAdapterCurrent:  "adapter current pointer",
	KindAdapterManifest: "adapter manifest",
}

// String names the kind in words.
func (k FileKind) String() string {
	if int(k) >= len(kindNames) || kindNames[k] == "" {
		return "unknown"
	}
	return kindNames[k]
}

// FileReason says why a file was refused.
type FileReason uint8

// The ways loading a file fails.
const (
	// FileMissing: a file or directory that must exist does not.
	FileMissing FileReason = iota + 1
	// FileUnreadable: the file exists and could not be read.
	FileUnreadable
	// FileMalformed: the content is not a document of the file's shape: not
	// JSON, an unknown, repeated, missing or null field, or a value of the
	// wrong type.
	FileMalformed
	// FileInvalid: the document is well formed and a value breaks a rule.
	FileInvalid
	// FileMisnamed: the name of the file does not match what it holds.
	FileMisnamed
)

var reasonNames = [...]string{
	FileMissing:    "missing",
	FileUnreadable: "unreadable",
	FileMalformed:  "malformed",
	FileInvalid:    "invalid",
	FileMisnamed:   "misnamed",
}

// String names the reason.
func (r FileReason) String() string {
	if int(r) >= len(reasonNames) || reasonNames[r] == "" {
		return "unknown"
	}
	return reasonNames[r]
}

// FileError is what a refused file gives: the node does not start, and the
// message names the file, so the owner knows what to fix.
type FileError struct {
	// Path is relative to the data directory root.
	Path   string
	Kind   FileKind
	Reason FileReason
	// Field is the JSON path of the offending value inside the file; empty when
	// the file as a whole is meant.
	Field string
	Err   error
}

func (e *FileError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "config: %s (%s) is %s", e.Path, e.Kind, e.Reason)
	if e.Field != "" {
		fmt.Fprintf(&b, " at %s", e.Field)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *FileError) Unwrap() error { return e.Err }

// FieldError is a value of a document, or a setting, that is refused.
type FieldError struct {
	// Field is the JSON path of the value.
	Field string
	// Malformed is true when the input is not a document of the expected shape
	// (see FileMalformed); false when a well formed value breaks a rule.
	Malformed bool
	Err       error
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return e.Err.Error()
	}
	return e.Field + ": " + e.Err.Error()
}

func (e *FieldError) Unwrap() error { return e.Err }

// ChangeReason says why a change of the configuration was refused.
type ChangeReason uint8

// The reasons a change is refused. Nothing is written for any of them.
const (
	// ChangeInvalid: a value is malformed or breaks a rule; Field names it.
	ChangeInvalid ChangeReason = iota + 1
	// ChangeVersionConflict: the version the request was based on is not the
	// current one; Current has the current one.
	ChangeVersionConflict
	// ChangePresetExists: a preset of this name exists.
	ChangePresetExists
	// ChangePresetUnknown: no preset of this name.
	ChangePresetUnknown
	// ChangeInstanceUnknown: no adapter instance of this name.
	ChangeInstanceUnknown
	// ChangeInstanceExists: enabling an existing instance with a configuration
	// would silently replace or ignore it.
	ChangeInstanceExists
	// ChangeInstanceAdapterMismatch: the existing instance belongs to another
	// adapter.
	ChangeInstanceAdapterMismatch
	// ChangeEndpointUnknown: the endpoint is neither a stable endpoint of the
	// instance nor open, so a setting for it could not be kept.
	ChangeEndpointUnknown
	// ChangeAdapterNotInstalled: the adapter has no installed package.
	ChangeAdapterNotInstalled
	// ChangeAdapterInUse: an upgrade of an adapter that still has an enabled
	// instance.
	ChangeAdapterInUse
	// ChangeNothingToChange: a patch that names no setting.
	ChangeNothingToChange
)

var changeNames = [...]string{
	ChangeInvalid:                 "invalid",
	ChangeVersionConflict:         "version_conflict",
	ChangePresetExists:            "preset_exists",
	ChangePresetUnknown:           "preset_unknown",
	ChangeInstanceUnknown:         "instance_unknown",
	ChangeInstanceExists:          "instance_exists",
	ChangeInstanceAdapterMismatch: "instance_adapter_mismatch",
	ChangeEndpointUnknown:         "endpoint_unknown",
	ChangeAdapterNotInstalled:     "adapter_not_installed",
	ChangeAdapterInUse:            "adapter_in_use",
	ChangeNothingToChange:         "nothing_to_change",
}

// String names the reason.
func (r ChangeReason) String() string {
	if int(r) >= len(changeNames) || changeNames[r] == "" {
		return "unknown"
	}
	return changeNames[r]
}

// ChangeError is a refused change.
type ChangeError struct {
	Reason ChangeReason
	// Field is the JSON path of the offending value, for ChangeInvalid.
	Field string
	// Current is the current version of the collection, for
	// ChangeVersionConflict.
	Current admin.Version
	Err     error
}

func (e *ChangeError) Error() string {
	s := "config: change refused: " + e.Reason.String()
	if e.Field != "" {
		s += " at " + e.Field
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *ChangeError) Unwrap() error { return e.Err }

// ErrOutOfRange is wrapped by the error of a setting whose own rule is
// violated: the settings whose limits another package owns return that
// package's error instead.
var ErrOutOfRange = errors.New("value out of range")

// ErrDiskReserveTooLow is wrapped by the error of a disk reserve that the
// unfinished deliveries and the logs could fill: new work would be refused only
// after the disk was already full.
var ErrDiskReserveTooLow = errors.New("disk reserve below what the node queue and the logs can fill")
