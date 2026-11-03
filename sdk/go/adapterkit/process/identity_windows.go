package process

import (
	"errors"
	"fmt"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openAccess is what a process needs to be described: its times and its end.
const openAccess = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE

// filetimeEpoch is 1970-01-01 in FILETIME units, 100 ns intervals since 1601.
const filetimeEpoch = 116444736000000000

// describe reads the identity of the process behind an open handle. The start key
// is the creation time as FILETIME, in decimal.
func describe(h windows.Handle, pid int) (Process, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return Process{}, fmt.Errorf("process: read the start of process %d: %w", pid, err)
	}
	ft := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	text := strconv.FormatUint(ft, 10)
	return Process{
		Identity:    Identity{PID: pid, StartKey: text},
		StartTime:   time.Unix(0, (int64(ft)-filetimeEpoch)*100),
		NativeStart: text,
	}, nil
}

// running reports whether the process behind an open handle has not ended. A
// process object outlives its process for as long as a handle to it is open, and
// OpenProcess still finds it, so the handle's signalled state is what tells.
func running(h windows.Handle) (bool, error) {
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false, err
	}
	return ev == uint32(windows.WAIT_TIMEOUT), nil
}

func lookup(pid int, includeEnded bool) (Process, bool, error) {
	h, err := windows.OpenProcess(openAccess, false, uint32(pid))
	switch {
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		return Process{}, false, nil
	case err != nil:
		return Process{}, false, fmt.Errorf("process: look up process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if !includeEnded {
		alive, err := running(h)
		if err != nil {
			return Process{}, false, fmt.Errorf("process: look up process %d: %w", pid, err)
		}
		if !alive {
			return Process{}, false, nil
		}
	}
	p, err := describe(h, pid)
	if err != nil {
		return Process{}, false, err
	}
	return p, true, nil
}

func list() ([]Process, error) {
	self, err := currentUser()
	if err != nil {
		return nil, err
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("process: list processes: %w", err)
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []Process
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		p, ok, err := listed(int(entry.ProcessID), self)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, p)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("process: list processes: %w", err)
	}
	return out, nil
}

func currentUser() (*windows.SID, error) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		return nil, fmt.Errorf("process: read the user of this process: %w", err)
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("process: read the user of this process: %w", err)
	}
	return u.User.Sid, nil
}

// listed describes the running process with pid when it belongs to the user with
// SID self. ok is false for a process that is not ours, that ended, or whose
// details are denied: the System and Idle processes, protected processes and
// those of a higher integrity level.
func listed(pid int, self *windows.SID) (p Process, ok bool, err error) {
	if pid == 0 {
		return Process{}, false, nil
	}
	h, err := windows.OpenProcess(openAccess, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return Process{}, false, nil
		}
		return Process{}, false, fmt.Errorf("process: look up process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		// Denied, or the process ended: not one of ours to list.
		return Process{}, false, nil
	}
	u, err := tok.GetTokenUser()
	tok.Close()
	if err != nil || !windows.EqualSid(u.User.Sid, self) {
		return Process{}, false, nil
	}
	cmdline, err := commandLine(h)
	if err != nil {
		if alive, aerr := running(h); aerr == nil && !alive {
			return Process{}, false, nil
		}
		if errors.Is(err, windows.STATUS_ACCESS_DENIED) {
			return Process{}, false, nil
		}
		return Process{}, false, fmt.Errorf("process: read the arguments of process %d: %w", pid, err)
	}
	alive, err := running(h)
	if err != nil {
		return Process{}, false, fmt.Errorf("process: look up process %d: %w", pid, err)
	}
	if !alive {
		return Process{}, false, nil
	}
	p, err = describe(h, pid)
	if err != nil {
		return Process{}, false, err
	}
	p.Args, err = windows.DecomposeCommandLine(cmdline)
	if err != nil {
		return Process{}, false, fmt.Errorf("process: read the arguments of process %d: %w", pid, err)
	}
	return p, true, nil
}

// processCommandLineInformation is the NtQueryInformationProcess class that returns
// the command line as a UNICODE_STRING followed by its characters.
const processCommandLineInformation = 60

func commandLine(h windows.Handle) (string, error) {
	buf := make([]byte, 4096)
	for range 3 {
		var need uint32
		err := windows.NtQueryInformationProcess(h, processCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &need)
		if err == nil {
			return (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String(), nil
		}
		if !errors.Is(err, windows.STATUS_BUFFER_OVERFLOW) && !errors.Is(err, windows.STATUS_BUFFER_TOO_SMALL) &&
			!errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) || int(need) <= len(buf) {
			return "", err
		}
		buf = make([]byte, need)
	}
	return "", errors.New("the command line keeps growing")
}
