package proc

import (
	"context"
	"os"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
)

type osTable struct{}

// NewTable returns the process table of the operating system.
func NewTable() Table { return osTable{} }

func (osTable) Lookup(pid int) (Info, bool, error) {
	p, found, err := process.Lookup(pid)
	if err != nil || !found {
		return Info{}, false, err
	}
	return infoOf(p), true, nil
}

func (osTable) Running(id Identity) (bool, error) { return process.Running(id) }

func (osTable) List() ([]Entry, error) {
	ps, err := process.List()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, len(ps))
	for i, p := range ps {
		out[i] = Entry{Info: infoOf(p), Args: p.Args}
	}
	return out, nil
}

type osLauncher struct{}

// NewLauncher returns the launcher of the operating system.
func NewLauncher() Launcher { return osLauncher{} }

func (osLauncher) Start(l Launch) (Started, error) {
	in, err := os.Open(l.Stdin)
	if err != nil {
		return Started{}, err
	}
	defer in.Close()
	out, err := os.OpenFile(l.Stdout, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return Started{}, err
	}
	defer out.Close()
	errf, err := os.OpenFile(l.Stderr, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return Started{}, err
	}
	defer errf.Close()

	s, err := process.Start(process.Spec{
		Argv: l.Argv, Env: l.Env, Dir: l.Dir, Stdin: in, Stdout: out, Stderr: errf,
		Interruptible: true,
	})
	if err != nil {
		return Started{}, err
	}
	return Started{Info: infoOf(s.Process), Container: s.Container, Detachment: s.Detachment, Done: s.Done}, nil
}

func (osLauncher) Host() Detachment { return process.HostDetachment() }

type osSignaller struct{}

// NewSignaller returns the signaller of the operating system.
func NewSignaller() Signaller { return osSignaller{} }

func (osSignaller) Interrupt(ctx context.Context, id Identity) error {
	return process.Interrupt(ctx, id)
}
