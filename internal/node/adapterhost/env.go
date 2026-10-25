package adapterhost

import (
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/logfile"
)

// Source is what the host reads of the configuration: which instances the owner
// enabled and which adapter packages are installed. It is read-only; every
// change of the configuration is an operation of the engine.
type Source interface {
	Instances() config.Instances
	Adapters() []config.Adapter
}

// LogOpener opens the log of an adapter instance by name; the daemon's handle for
// the logs under logs/ is one. The share of the log budget of each log is the
// handle's, so the host never counts instances or divides anything.
type LogOpener interface {
	Open(name string) (*logfile.Log, error)
}

// Env is what the host needs of the node when it starts.
type Env struct {
	// Home is the data directory, as an absolute path of the operating system's
	// file system. The instance lock files are under it, and so are the installed
	// adapter packages. The host only builds paths from it: it touches no file.
	Home string
	// Source is the configuration.
	Source Source
	// Logs opens the log of each instance (adapter-<instance>.log).
	Logs LogOpener
	// Engine is what the host asks of the engine. If the value also implements
	// EndpointCalls or ObservedCalls, the host answers those requests of the
	// adapters through it.
	Engine Engine
}
