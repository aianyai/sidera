//go:build !darwin && !linux && !windows

package process

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Only macOS, Linux and Windows have a process table, and a process that cannot be
// named is not started: Start fails before anything runs, so that no process is
// left behind whose identity could never be read.

func launch(Spec, stdio) (*exec.Cmd, adapterproto.Container, Detachment, error) {
	return nil, adapterproto.Container{}, Detachment{}, fmt.Errorf("%w: starting a process", ErrUnsupported)
}

func interrupt(context.Context, Identity) error {
	return fmt.Errorf("%w: interrupting a process", ErrUnsupported)
}

func hostDetachment() Detachment {
	return Detachment{Caveat: "starting a detached process is not supported on this platform"}
}
