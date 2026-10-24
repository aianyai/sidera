package ipc

import (
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
)

// listen creates the pipe. go-winio creates the first instance with
// FILE_CREATE, the NT counterpart of FILE_FLAG_FIRST_PIPE_INSTANCE, so a name
// that already exists makes it fail: nobody can serve the name beside us, and
// we cannot take over a name someone else holds. It always rejects remote
// clients. The security descriptor lets the user, and only the user, open the
// pipe; the kernel enforces it at open time, so the listener does not check
// who connects.
//
// A pipe is a kernel object that vanishes when its last handle closes, so there
// is no leftover to delete here.
func listen(addr Address, opts Options) (net.Listener, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, fmt.Errorf("ipc: build the pipe security descriptor: %w", err)
	}
	l, err := winio.ListenPipe(addr.name, &winio.PipeConfig{SecurityDescriptor: pipeSDDL(sid.String())})
	if err != nil {
		return nil, fmt.Errorf("ipc: listen on pipe %s: %w", addr.name, err)
	}
	return l, nil
}
