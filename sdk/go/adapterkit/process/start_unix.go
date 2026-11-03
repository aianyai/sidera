//go:build unix

package process

import (
	"os/exec"
	"strconv"
	"syscall"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// launch starts the process in a session of its own: no controlling terminal, a
// process group whose ID is the PID, and a Ctrl-C or a hang-up in the adapter's
// terminal does not reach it.
func launch(spec Spec, s stdio) (*exec.Cmd, adapterproto.Container, Detachment, error) {
	argv, container, detachment, err := place(spec)
	if err != nil {
		return nil, adapterproto.Container{}, Detachment{}, err
	}
	cmd := command(spec, argv, s, &syscall.SysProcAttr{Setsid: true})
	if err := cmd.Start(); err != nil {
		return nil, adapterproto.Container{}, Detachment{}, err
	}
	if container.Kind == adapterproto.ContainerProcessGroup {
		container.Name = strconv.Itoa(cmd.Process.Pid)
	}
	return cmd, container, detachment, nil
}
