package config

import (
	"errors"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// DefaultPresetName is the one preset `init` writes, and the description it
// carries.
const (
	DefaultPresetName        = "default"
	defaultPresetDescription = "Created by sidera init."
)

// initFile is a file `init` has to write.
type initFile struct {
	role string
	path string
	data []byte
}

// InitPlan is what `init` has to do on the side of config/. `init` never
// overwrites: for each of the four files it writes, a file that is missing is
// written with the defaults, one that exists and is valid stays as the owner
// left it, and one that exists and is not valid makes the plan fail, naming
// the file, so that the owner decides what becomes of it.
type InitPlan struct {
	dirs  []string
	files []initFile
}

// PlanInit checks what is on disk and decides what is missing. It only reads, so
// when it fails nothing has been written.
func PlanInit(r fsx.Reader) (*InitPlan, error) {
	p := &InitPlan{}
	for _, dir := range LayoutDirs() {
		info, err := r.Stat(dir)
		switch {
		case err == nil && info.IsDir:
		case err == nil:
			return nil, &FileError{Path: dir, Kind: KindDir, Reason: FileInvalid, Err: errors.New("is not a directory")}
		case missing(err):
			p.dirs = append(p.dirs, dir)
		default:
			return nil, &FileError{Path: dir, Kind: KindDir, Reason: FileUnreadable, Err: err}
		}
	}

	defaults := []struct {
		role, path string
		kind       FileKind
		check      func([]byte) error
		build      func() ([]byte, error)
	}{
		{RoleNode, NodePath, KindNode,
			func(b []byte) error { _, err := DecodeNode(b); return err },
			func() ([]byte, error) { return Node{Version: 1, Settings: DefaultNodeSettings()}.Encode() }},
		{RolePolicy, PolicyPath, KindPolicy,
			func(b []byte) error { _, err := DecodePolicy(b); return err },
			func() ([]byte, error) { return PolicyFile{Version: 1, Policy: authz.DefaultPolicy()}.Encode() }},
		{RolePresets, PresetsPath, KindPresets,
			func(b []byte) error { _, err := DecodePresets(b); return err },
			func() ([]byte, error) {
				return Presets{Version: 1, Items: map[string]Preset{
					DefaultPresetName: {LocalSources: authz.PresetLocalAllow, Description: defaultPresetDescription},
				}}.Encode()
			}},
		{RoleInstances, InstancesPath, KindInstances,
			func(b []byte) error { _, err := DecodeInstances(b); return err },
			func() ([]byte, error) { return Instances{Version: 1, Items: map[string]Instance{}}.Encode() }},
	}
	for _, d := range defaults {
		data, err := r.ReadFile(d.path)
		switch {
		case err == nil:
			if err := d.check(data); err != nil {
				return nil, err
			}
		case missing(err):
			data, err := d.build()
			if err != nil {
				return nil, err
			}
			p.files = append(p.files, initFile{role: d.role, path: d.path, data: data})
		default:
			return nil, &FileError{Path: d.path, Kind: d.kind, Reason: FileUnreadable, Err: err}
		}
	}
	return p, nil
}

// MakeDirs creates the directories of config/ that are missing, each after its
// parent, under RoleDir. It is a part of the transaction of `init`, which the
// initialisation entry declares, with the other owners' directories made under
// the same role before the files.
func (p *InitPlan) MakeDirs(tx *txn.Tx) error {
	for _, dir := range p.dirs {
		if err := tx.MakeDir(RoleDir, dir); err != nil {
			return err
		}
	}
	return nil
}

// WriteFiles writes the files that are missing, in the order node, policy,
// presets, instances, each under its role.
func (p *InitPlan) WriteFiles(tx *txn.Tx) error {
	for _, f := range p.files {
		if err := tx.Replace(f.role, f.path, f.data, fsx.ModePrivate); err != nil {
			return err
		}
	}
	return nil
}
