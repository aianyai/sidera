package ipc

import (
	"errors"
	"fmt"
	"path/filepath"
)

// Kind names one of the two local interfaces.
type Kind string

const (
	// KindAgent is the interface that agents and the CLI's agent commands use.
	KindAgent Kind = "agent"
	// KindManage is the management interface of the local owner.
	KindManage Kind = "manage"
)

// socketDir is the directory under the data directory that holds the socket of
// the interface. The management socket is kept out of run/ because sandboxes
// are granted access by directory and run/ is granted to agents.
func (k Kind) socketDir() (string, bool) {
	switch k {
	case KindAgent:
		return "run", true
	case KindManage:
		return "manage", true
	}
	return "", false
}

// Address is where one interface of one data directory is reached: a socket
// path on Unix, a pipe name on Windows. It can only be made by Locate, which
// checks that the path fits the platform, so every holder of an Address has
// passed the same check.
type Address struct {
	kind Kind
	home string
	name string
}

// Kind reports which interface the address belongs to.
func (a Address) Kind() Kind { return a.kind }

// Name is the socket path (Unix) or the pipe name (Windows).
func (a Address) Name() string { return a.name }

func (a Address) String() string { return fmt.Sprintf("%s interface at %s", a.kind, a.name) }

// absoluteHome makes the data directory absolute and clean, which is how every
// process derives the same address from the same --home value.
func absoluteHome(home string) (string, error) {
	if home == "" {
		return "", errors.New("ipc: the data directory is not set")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("ipc: make the data directory %q absolute: %w", home, err)
	}
	return filepath.Clean(abs), nil
}
