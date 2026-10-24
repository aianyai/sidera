//go:build unix

package ipc

import (
	"fmt"
	"path/filepath"
)

// Locate derives the address of the interface of the data directory home: the
// socket <home>/run/agent.sock or <home>/manage/manage.sock. home is made
// absolute and clean first, so every process derives the same path from the
// same value. A path longer than the platform's limit is an error that names
// the path, its length and the limit.
func Locate(home string, kind Kind) (Address, error) {
	dir, ok := kind.socketDir()
	if !ok {
		return Address{}, fmt.Errorf("ipc: unknown interface %q", kind)
	}
	abs, err := absoluteHome(home)
	if err != nil {
		return Address{}, err
	}
	name := filepath.Join(abs, dir, string(kind)+".sock")
	if err := checkSocketPathLength(name, platformSocketPathLimit); err != nil {
		return Address{}, err
	}
	return Address{kind: kind, home: abs, name: name}, nil
}
