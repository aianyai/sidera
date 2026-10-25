package adapterhost

import (
	"context"
	"errors"
	"io"

	"github.com/aianyai/sidera/internal/node/config"
)

// Instance is what a launcher is told to start.
type Instance struct {
	// ID is the name of the instance.
	ID string
	// Adapter is the installed package the instance runs; its manifest names the
	// program and the arguments.
	Adapter config.Adapter
	// Dir is the directory of the installed package version, as a path of the
	// operating system's file system: what the program in the manifest is
	// relative to.
	Dir string
	// LockPath is the file the adapter locks before it answers initialize. The
	// host sends it in initialize; a launcher that needs it (a fake that is told
	// where to lock) has it here too.
	LockPath string
	// Stderr receives what the adapter writes on its standard error, which is its
	// log: lines, never protocol. It is never nil.
	Stderr io.Writer
}

// Launcher starts one adapter instance and hands back its protocol stream.
type Launcher interface {
	Launch(ctx context.Context, inst Instance) (Process, error)
}

// Process is an adapter that was started: a child process, or one that runs in
// the test process over a pair of pipes.
type Process interface {
	// Stream returns the adapter's standard output, to read from, and its standard
	// input, to write to.
	Stream() (io.Reader, io.WriteCloser)
	// PID is the process id; 0 for an adapter that runs in the test process.
	PID() int
	// Kill ends the process now. It may be called more than once.
	Kill() error
	// Wait returns when the process has ended.
	Wait() error
}

// ErrNotConnected is what a call to an adapter that is not in service returns
// without sending anything: no connection, or one that has not finished
// adapter.start.
var ErrNotConnected = errors.New("adapterhost: the adapter is not connected")
