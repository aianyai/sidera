package adapterhost

import (
	"log/slog"

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
	// adapter packages and the state directories of the instances. The host builds
	// paths from it and makes the state directories; it touches no other file.
	Home string
	// Source is the configuration.
	Source Source
	// Logs opens the log of each instance (adapter-<instance>.log).
	Logs LogOpener
	// Engine is what the host asks of the engine. If the value also implements
	// EndpointCalls, ObservedCalls or DirectoryCalls, the host answers those
	// requests of the adapters through it.
	Engine Engine
	// Log receives what the host did and why, among it the error that ended each
	// attempt to start an instance; nil discards it. It never receives a message
	// body, a token or a key. It is part of Env, not of Config, because the daemon
	// that owns the log is built after the host is.
	Log *slog.Logger
}
