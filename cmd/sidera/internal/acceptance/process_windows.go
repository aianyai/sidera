package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A job belongs to the fixture, not to a daemon generation. No breakaway flags
// are enabled, so even a descendant's new process group remains in this job.
type processScope struct {
	mu        sync.Mutex
	job       windows.Handle
	closed    bool
	processes []*process
	err       error
}

func newProcessScope() (*processScope, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("acceptance: create process job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, errors.Join(err, windows.CloseHandle(job))
	}
	return &processScope{job: job}, nil
}

func (s *processScope) start(spec processSpec) (*process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("acceptance: process scope is closed")
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	pi, err := createSuspended(spec)
	if err != nil {
		return nil, err
	}
	return s.resumeSuspended(pi)
}

// resumeSuspended consumes both creation handles, including on failure. Its
// caller holds the scope lock so close cannot race admission or resumption.
func (s *processScope) resumeSuspended(pi *windows.ProcessInformation) (*process, error) {
	defer windows.CloseHandle(pi.Thread)
	fail := func(cause error) (*process, error) {
		return nil, errors.Join(cause, terminateStarted(pi.Process), windows.CloseHandle(pi.Process))
	}
	if err := windows.AssignProcessToJobObject(s.job, pi.Process); err != nil {
		return fail(fmt.Errorf("acceptance: assign suspended process to job: %w", err))
	}
	// FindProcess obtains its own real handle while the original process handle
	// still pins this identity and the primary thread cannot run.
	native, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		return fail(err)
	}
	if _, err = windows.ResumeThread(pi.Thread); err != nil {
		_, failure := fail(err)
		return nil, errors.Join(failure, native.Release())
	}
	p := &process{native: native, done: make(chan struct{})}
	s.processes = append(s.processes, p)
	go func() {
		state, err := native.Wait()
		if err == nil && !state.Success() {
			err = &exec.ExitError{ProcessState: state}
		}
		p.err = errors.Join(err, windows.CloseHandle(pi.Process))
		close(p.done)
	}()
	return p, nil
}

// createSuspended owns no process after an error, and returns a process whose
// primary thread has never run. Only duplicate stdio handles are inheritable.
func createSuspended(spec processSpec) (*windows.ProcessInformation, error) {
	exe, err := windows.UTF16PtrFromString(spec.exe)
	if err != nil {
		return nil, err
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{spec.exe}, spec.args...)))
	if err != nil {
		return nil, err
	}
	dir, err := windows.UTF16PtrFromString(spec.dir)
	if err != nil {
		return nil, err
	}
	env := append([]string{}, spec.env...)
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	in, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	handles := make([]windows.Handle, 3)
	defer func() {
		for _, h := range handles {
			if h != 0 {
				windows.CloseHandle(h)
			}
		}
	}()
	for i, file := range []*os.File{in, spec.stdout, spec.stderr} {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(), &handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return nil, err
		}
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return nil, err
	}
	si := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES, StdInput: handles[0], StdOutput: handles[1], StdErr: handles[2]}, ProcThreadAttributeList: attrs.List()}
	pi := new(windows.ProcessInformation)
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcess(exe, line, nil, nil, true, flags, &block[0], dir, &si.StartupInfo, pi); err != nil {
		return nil, err
	}
	return pi, nil
}

func terminateStarted(handle windows.Handle) error {
	err := windows.TerminateProcess(handle, 1)
	status, waitErr := windows.WaitForSingleObject(handle, 10000)
	if waitErr == nil && status != windows.WAIT_OBJECT_0 {
		waitErr = fmt.Errorf("acceptance: terminated process wait returned %d", status)
	}
	return errors.Join(err, waitErr)
}

// x/sys exposes the information class but not this Win32 structure.
type jobAccounting struct {
	totalUserTime, totalKernelTime, periodUserTime, periodKernelTime int64
	pageFaults, totalProcesses, activeProcesses, terminatedProcesses uint32
}

func (s *processScope) activeProcesses() (uint32, error) {
	var info jobAccounting
	err := windows.QueryInformationJobObject(s.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return info.activeProcesses, err
}

func (s *processScope) close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.Join(ctx.Err(), s.err)
	}
	s.closed = true
	// Caller cancellation remains an error, but cannot abandon owned children.
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.err = windows.TerminateJobObject(s.job, 1)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		active, err := s.activeProcesses()
		if err != nil {
			s.err = errors.Join(s.err, err)
			break
		}
		if active == 0 {
			break
		}
		select {
		case <-tick.C:
		case <-cleanup.Done():
			// A final query, rather than an event or elapsed time, proves emptiness.
			active, err = s.activeProcesses()
			if err != nil || active != 0 {
				s.err = errors.Join(s.err, fmt.Errorf("acceptance: process job still has %d active processes: %w", active, cleanup.Err()), err)
			}
			goto waited
		}
	}
waited:
	for _, p := range s.processes {
		select {
		case <-p.done:
		case <-cleanup.Done():
			s.err = errors.Join(s.err, cleanup.Err())
		}
	}
	s.err = errors.Join(s.err, windows.CloseHandle(s.job))
	s.job = 0
	return errors.Join(ctx.Err(), s.err)
}
