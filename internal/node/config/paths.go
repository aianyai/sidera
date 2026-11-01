package config

import (
	"strconv"

	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// The files of config/, relative to the data directory root.
const (
	Dir            = "config"
	NodePath       = Dir + "/node.json"
	PolicyPath     = Dir + "/policy.json"
	PresetsPath    = Dir + "/presets.json"
	InstancesPath  = Dir + "/instances.json"
	NetworkPath    = Dir + "/network.json"
	EndpointIDsDir = Dir + "/endpoint-ids"
	BackupPath     = Dir + "/revocation-backup.json"
	RevocationsDir = Dir + "/revocations"
	AdaptersDir    = Dir + "/adapters"
)

const (
	currentFile = "current"
	versionsDir = "versions"
	stagePrefix = ".stage-"
)

// LayoutDirs is the directories config/ consists of, each after its parent.
// `init` creates them and loading refuses a tree that lacks one.
func LayoutDirs() []string {
	return []string{Dir, EndpointIDsDir, RevocationsDir, AdaptersDir}
}

// EndpointIDsPath is the file of the stable endpoint ids of an instance.
func EndpointIDsPath(instance string) string { return EndpointIDsDir + "/" + instance + ".json" }

// RevocationPath is the file of a revocation record.
func RevocationPath(d record.Digest) string { return RevocationsDir + "/" + d.String() + ".rec" }

// AdapterDir is the directory of an adapter: its versions and its pointer.
func AdapterDir(id string) string { return AdaptersDir + "/" + id }

// AdapterCurrentPath is the pointer file that names the installed version.
func AdapterCurrentPath(id string) string { return AdapterDir(id) + "/" + currentFile }

// AdapterVersionsDir holds the version directories and the staging directories.
func AdapterVersionsDir(id string) string { return AdapterDir(id) + "/" + versionsDir }

// AdapterVersionDir is the immutable package directory of one version.
func AdapterVersionDir(id, version string) string { return AdapterVersionsDir(id) + "/" + version }

// AdapterRevisionDir is the immutable package directory of one version at one
// revision. Revision 0 is the directory of the version itself; installing the
// same version again with other contents needs a directory of another name, so
// that the pointer's replacement is what switches packages and no directory is
// ever replaced in place. The "~" cannot be part of a version.
func AdapterRevisionDir(id, version string, revision uint32) string {
	if revision == 0 {
		return AdapterVersionDir(id, version)
	}
	return AdapterVersionDir(id, version) + "~" + strconv.FormatUint(uint64(revision), 10)
}

// AdapterManifestPath is the manifest of the first package of one version.
func AdapterManifestPath(id, version string) string {
	return AdapterVersionDir(id, version) + "/" + manifest.FileName
}

// AdapterStageDir is the staging directory of an installation, next to the
// versions so that it can be renamed into one.
func AdapterStageDir(id, suffix string) string {
	return AdapterVersionsDir(id) + "/" + stagePrefix + suffix
}
