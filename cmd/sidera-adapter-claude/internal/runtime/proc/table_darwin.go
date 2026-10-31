package proc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// szomb is the process state of a zombie in <sys/proc.h>.
const szomb = 5

type osTable struct{}

// NewTable returns the process table of the operating system.
func NewTable() Table { return osTable{} }

func info(kp *unix.KinfoProc) Info {
	return Info{
		PID:        int(kp.Proc.P_pid),
		Alive:      kp.Proc.P_stat != szomb,
		StartUnix:  int64(kp.Proc.P_starttime.Sec),
		StartMicro: int64(kp.Proc.P_starttime.Usec),
	}
}

func (osTable) Lookup(pid int) (Info, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return Info{}, fmt.Errorf("proc: look up process %d: %w", pid, err)
	}
	if len(kps) == 0 || int(kps[0].Proc.P_pid) != pid {
		return Info{PID: pid}, nil
	}
	return info(&kps[0]), nil
}

func (osTable) Snapshot() (Snapshot, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("proc: list processes: %w", err)
	}
	uid := uint32(os.Geteuid())
	var out Snapshot
	for i := range kps {
		kp := &kps[i]
		if kp.Eproc.Ucred.Uid != uid {
			continue
		}
		e := Entry{Info: info(kp)}
		if e.Alive {
			args, err := procArgs(e.PID)
			switch {
			case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ESRCH):
				// The process ended between the two reads.
				continue
			case err != nil:
				return nil, fmt.Errorf("proc: read the arguments of process %d: %w", e.PID, err)
			}
			e.Args = args
		}
		out = append(out, e)
	}
	return out, nil
}

// procArgs reads the argument vector of a process: kern.procargs2 holds the
// argument count, the executable path, padding NULs and then the arguments and
// the environment, each ended by a NUL.
func procArgs(pid int) ([]string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(raw) < 4 {
		return nil, fmt.Errorf("short procargs2 of %d bytes", len(raw))
	}
	argc := int(binary.NativeEndian.Uint32(raw))
	rest := raw[4:]
	exec := strings.IndexByte(string(rest), 0)
	if exec < 0 {
		return nil, errors.New("procargs2 without an executable path")
	}
	rest = rest[exec:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	args := make([]string, 0, argc)
	for len(args) < argc {
		end := strings.IndexByte(string(rest), 0)
		if end < 0 {
			return nil, errors.New("procargs2 ends inside the arguments")
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return args, nil
}
