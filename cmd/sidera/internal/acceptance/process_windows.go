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
	// Keep the complete-environment contract even for nil, while applying
	// exec's case-insensitive last-value-wins and SYSTEMROOT handling.
	env := (&exec.Cmd{Env: append([]string{}, spec.env...), Dir: spec.dir}).Environ()
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return waitTerminatedProcess(ctx, handle, windows.TerminateProcess(handle, 1))
}

func waitTerminatedProcess(ctx context.Context, handle windows.Handle, terminateErr error) error {
	waitErr := waitProcessExit(ctx, handle)
	// TerminateProcess can deny access when this process is already ending.
	// Only a successful wait on this exact handle proves that benign case.
	if waitErr == nil && errors.Is(terminateErr, windows.ERROR_ACCESS_DENIED) {
		terminateErr = nil
	}
	return errors.Join(terminateErr, waitErr)
}

// The cleanup context always has a deadline, shared by all process waits.
func waitProcessExit(ctx context.Context, handle windows.Handle) error {
	deadline, _ := ctx.Deadline()
	remaining := max(time.Until(deadline), 0)
	ms := uint32((remaining + time.Millisecond - 1) / time.Millisecond)
	status, err := windows.WaitForSingleObject(handle, ms)
	if err != nil {
		return err
	}
	if status == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("acceptance: process exit wait: %w", context.DeadlineExceeded)
	}
	if status != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("acceptance: process exit wait returned %d", status)
	}
	return nil
}

// x/sys exposes the information class but not this variable-length structure.
type jobProcessList struct {
	assigned, count uint32
	pids            [1]uintptr
}

func (s *processScope) processIDs(ctx context.Context) ([]uintptr, error) {
	const word = unsafe.Sizeof(uintptr(0))
	const header = unsafe.Offsetof(jobProcessList{}.pids) / word
	for capacity := uint32(16); ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		buffer := make([]uintptr, int(header)+int(capacity))
		info := (*jobProcessList)(unsafe.Pointer(&buffer[0]))
		err := windows.QueryInformationJobObject(s.job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(info)), uint32(uintptr(len(buffer))*word), nil)
		if errors.Is(err, windows.ERROR_MORE_DATA) || err == nil && info.assigned > info.count {
			capacity = max(capacity*2, info.assigned)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("acceptance: list process job: %w", err)
		}
		return buffer[header : header+uintptr(info.count)], nil
	}
}

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// openProcesses returns only handles verified against this job. The caller owns
// every returned handle, including when another listed PID cannot be opened.
func (s *processScope) openProcesses(pids []uintptr) ([]windows.Handle, error) {
	var handles []windows.Handle
	var errs []error
	for _, pid := range pids {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue // The process disappeared between enumeration and OpenProcess.
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("acceptance: open job process %d: %w", pid, err))
			continue
		}
		// A PID can be reused before OpenProcess. Pin the identity with its
		// handle and check membership before requesting any termination.
		var member int32
		ok, _, checkErr := isProcessInJob.Call(uintptr(handle), uintptr(s.job), uintptr(unsafe.Pointer(&member)))
		if ok == 0 {
			errs = append(errs, fmt.Errorf("acceptance: check job process %d: %w", pid, checkErr))
		}
		if ok == 0 || member == 0 {
			errs = append(errs, windows.CloseHandle(handle))
			continue
		}
		handles = append(handles, handle)
	}
	return handles, errors.Join(errs...)
}

func (s *processScope) reclaim(ctx context.Context) error {
	for {
		pids, err := s.processIDs(ctx)
		if err != nil || len(pids) == 0 {
			return err
		}
		handles, openErr := s.openProcesses(pids)
		errs := []error{openErr}
		// Request every termination before waiting, so all members can unwind
		// concurrently. Accounting zero after job termination is not an exit wait.
		termination := make([]error, len(handles))
		for i, handle := range handles {
			termination[i] = windows.TerminateProcess(handle, 1)
		}
		for i, handle := range handles {
			errs = append(errs, waitTerminatedProcess(ctx, handle, termination[i]), windows.CloseHandle(handle))
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
		// A member could have created descendants during the preceding round.
		// Enumerate again after actual exits rather than trusting one snapshot.
	}
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
	s.err = s.reclaim(cleanup)
	if s.err != nil {
		// Best-effort fallback; neither this request nor kill-on-close proves
		// actual exit, so the reclamation failure must remain visible.
		s.err = errors.Join(s.err, windows.TerminateJobObject(s.job, 1))
	}
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
