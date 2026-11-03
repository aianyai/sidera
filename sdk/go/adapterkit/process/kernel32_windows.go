package process

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The console and job calls that golang.org/x/sys/windows has no wrapper for.
var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole      = kernel32.NewProc("AttachConsole")
	procFreeConsole        = kernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHdlr = kernel32.NewProc("SetConsoleCtrlHandler")
	procIsProcessInJob     = kernel32.NewProc("IsProcessInJob")
)

// call runs a kernel32 function that returns a BOOL and sets the last error.
func call(p *windows.LazyProc, args ...uintptr) error {
	if r, _, err := p.Call(args...); r == 0 {
		return fmt.Errorf("%s: %w", p.Name, err)
	}
	return nil
}

func attachConsole(pid uint32) error { return call(procAttachConsole, uintptr(pid)) }
func freeConsole() error             { return call(procFreeConsole) }

// setConsoleCtrlHandler(nil, ignore) sets whether this process ignores Ctrl-C; the
// setting is inherited by the processes it starts.
func setConsoleCtrlHandler(ignore bool) error {
	var v uintptr
	if ignore {
		v = 1
	}
	return call(procSetConsoleCtrlHdlr, 0, v)
}

// currentJob says whether this process is in a job and, if it is, the limit flags
// of the job it is directly in. A process in a job nested inside others reads only
// the innermost one.
func currentJob() (inJob bool, limitFlags uint32, err error) {
	var in int32
	if err := call(procIsProcessInJob, uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&in))); err != nil {
		return false, 0, err
	}
	if in == 0 {
		return false, 0, nil
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var n uint32
	err = windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &n)
	if err != nil {
		return true, 0, fmt.Errorf("QueryInformationJobObject: %w", err)
	}
	return true, info.BasicLimitInformation.LimitFlags, nil
}
