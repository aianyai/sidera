package process

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// szomb is the process state of a zombie in <sys/proc.h>.
const szomb = 5

// fromKinfo reads one kern.proc entry. The start key is "<sec>.<usec, 6 digits>"
// and is the same text as the start key of the Claude adapter's first versions,
// so checkpoints written then stay valid.
func fromKinfo(kp *unix.KinfoProc) Process {
	sec, usec := int64(kp.Proc.P_starttime.Sec), int64(kp.Proc.P_starttime.Usec)
	start := fmt.Sprintf("%d.%06d", sec, usec)
	return Process{
		Identity:    Identity{PID: int(kp.Proc.P_pid), StartKey: start},
		StartTime:   time.Unix(sec, usec*1000),
		NativeStart: start,
	}
}

func lookup(pid int, includeEnded bool) (Process, bool, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return Process{}, false, fmt.Errorf("process: look up process %d: %w", pid, err)
	}
	if len(kps) == 0 || int(kps[0].Proc.P_pid) != pid {
		return Process{}, false, nil
	}
	if kps[0].Proc.P_stat == szomb && !includeEnded {
		return Process{}, false, nil
	}
	return fromKinfo(&kps[0]), true, nil
}

func list() ([]Process, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("process: list processes: %w", err)
	}
	uid := uint32(os.Geteuid())
	var out []Process
	for i := range kps {
		kp := &kps[i]
		if kp.Eproc.Ucred.Uid != uid || kp.Proc.P_stat == szomb {
			continue
		}
		p := fromKinfo(kp)
		args, err := procArgs(p.PID)
		switch {
		case unlistable(err):
			continue
		case err != nil:
			return nil, fmt.Errorf("process: read the arguments of process %d: %w", p.PID, err)
		}
		p.Args = args
		out = append(out, p)
	}
	return out, nil
}

// unlistable reports whether the error of reading the arguments of a process leaves
// it out of a listing: its details are denied, or it has ended or is ending. Any
// other error is a failure to ask.
//
// The kernel answers EINVAL for a PID it no longer finds, for a process that has
// no task or user stack any more and for one of another user; EIO when it cannot
// copy the argument area out of the process's address space, which is what a
// process that is exiting looks like while its memory is torn down (and which is
// not yet a zombie, so the state in the process list does not show it). The sysctl
// layer itself refuses with EPERM or EACCES. Lookup never meets these: it reads
// no arguments.
func unlistable(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EIO) ||
		errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES)
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
