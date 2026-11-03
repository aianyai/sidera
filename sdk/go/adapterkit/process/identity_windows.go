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

// fromFiletime is the process pid that was created at the FILETIME ft. The start
// key is the creation time as FILETIME, in decimal.
func fromFiletime(pid int, ft uint64) Process {
	text := strconv.FormatUint(ft, 10)
	return Process{
		Identity:    Identity{PID: pid, StartKey: text},
		StartTime:   time.Unix(0, (int64(ft)-filetimeEpoch)*100),
		NativeStart: text,
	}
}

// describe reads the identity of the process behind an open handle.
func describe(h windows.Handle, pid int) (Process, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return Process{}, fmt.Errorf("process: read the start of process %d: %w", pid, err)
	}
	return fromFiletime(pid, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime)), nil
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

// openOutcome is how OpenProcess's error classifies a process.
type openOutcome int

const (
	opened  openOutcome = iota // no error
	absent                     // no process holds the PID
	refused                    // the process exists and may not be opened
	failed                     // the platform did not answer
)

// classifyOpen sorts the error of OpenProcess. Only ERROR_INVALID_PARAMETER means
// that no process holds the PID; a refusal is not that, it is a process that runs
// beyond the adapter's reach, and taking it for gone starts a second writer.
func classifyOpen(err error) openOutcome {
	switch {
	case err == nil:
		return opened
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		return absent
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return refused
	}
	return failed
}

func lookup(pid int, includeEnded bool) (Process, bool, error) {
	h, err := windows.OpenProcess(openAccess, false, uint32(pid))
	switch classifyOpen(err) {
	case absent:
		return Process{}, false, nil
	case refused:
		return lookupRefused(pid, err, systemStart)
	case failed:
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

// lookupRefused answers for a process that may not be opened. It is an error that
// carries the start key when the system reports the start without a handle, so
// that Running can tell another process that holds the PID now. A process the
// system no longer lists has ended.
func lookupRefused(pid int, cause error, start func(pid int) (uint64, bool, error)) (Process, bool, error) {
	ft, listed, err := start(pid)
	switch {
	case err != nil:
		return Process{}, false, fmt.Errorf("process: look up process %d: %w (and its start could not be read: %v)", pid, cause, err)
	case !listed:
		return Process{}, false, nil
	}
	return Process{}, false, &deniedError{pid: pid, startKey: strconv.FormatUint(ft, 10), err: cause}
}

// systemStart reads the creation FILETIME of the process pid from the system's
// process information, which needs no handle to the process and so tells a start
// that OpenProcess refuses. listed is false when the system has no such process.
func systemStart(pid int) (filetime uint64, listed bool, err error) {
	buf := make([]byte, 1<<20)
	for range 8 {
		var need uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &need)
		if errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) {
			// The table grew since it was sized; ask for more than it needs now.
			buf = make([]byte, max(int(need), len(buf))+(1<<16))
			continue
		}
		if err != nil {
			return 0, false, fmt.Errorf("NtQuerySystemInformation: %w", err)
		}
		for off := 0; off+int(unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{})) <= len(buf); {
			e := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buf[off]))
			if int(e.UniqueProcessID) == pid {
				return uint64(e.CreateTime), true, nil
			}
			if e.NextEntryOffset == 0 {
				break
			}
			off += int(e.NextEntryOffset)
		}
		return 0, false, nil
	}
	return 0, false, errors.New("NtQuerySystemInformation: the process table keeps growing")
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
// those of a higher integrity level. Their owner cannot be read without opening
// them, so whoever owns them they are left out.
func listed(pid int, self *windows.SID) (p Process, ok bool, err error) {
	if pid == 0 {
		return Process{}, false, nil
	}
	h, err := windows.OpenProcess(openAccess, false, uint32(pid))
	switch classifyOpen(err) {
	case absent, refused:
		return Process{}, false, nil
	case failed:
		return Process{}, false, fmt.Errorf("process: look up process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		if unlistable(h, err) {
			return Process{}, false, nil
		}
		return Process{}, false, fmt.Errorf("process: read the user of process %d: %w", pid, err)
	}
	u, err := tok.GetTokenUser()
	tok.Close()
	if err != nil {
		if unlistable(h, err) {
			return Process{}, false, nil
		}
		return Process{}, false, fmt.Errorf("process: read the user of process %d: %w", pid, err)
	}
	if !windows.EqualSid(u.User.Sid, self) {
		return Process{}, false, nil
	}
	cmdline, err := commandLine(h)
	if err != nil {
		if unlistable(h, err) {
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

// unlistable reports whether the error of reading a process leaves it out of a
// listing: the read was denied, or the process has ended. Any other error is a
// failure to ask.
func unlistable(h windows.Handle, err error) bool {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.STATUS_ACCESS_DENIED) {
		return true
	}
	alive, aerr := running(h)
	return aerr == nil && !alive
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
