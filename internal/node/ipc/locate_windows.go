package ipc

import "fmt"

// Locate derives the address of the interface of the data directory home: the
// pipe \\.\pipe\sidera-<h>-agent or \\.\pipe\sidera-<h>-manage, where <h> is
// derived from the user's SID and the data directory (see pipeHash).
func Locate(home string, kind Kind) (Address, error) {
	if _, ok := kind.socketDir(); !ok {
		return Address{}, fmt.Errorf("ipc: unknown interface %q", kind)
	}
	abs, err := absoluteHome(home)
	if err != nil {
		return Address{}, err
	}
	sid, err := currentUserSID()
	if err != nil {
		return Address{}, fmt.Errorf("ipc: derive the pipe name: %w", err)
	}
	return Address{kind: kind, home: abs, name: pipeName(pipeHash(sid.String(), pipeHomeKey(abs)), kind)}, nil
}
