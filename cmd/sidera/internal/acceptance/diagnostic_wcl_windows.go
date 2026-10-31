//go:build acceptance && diagnostic && windows

package acceptance

import (
	"os"
	"time"
	"unsafe"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"golang.org/x/sys/windows"
)

// Keep the Windows ABI fixed even when the diagnostic is cross-compiled.
var _ [668 - unsafe.Sizeof(diagnosticWCLProcessInfo{})]byte
var _ [unsafe.Sizeof(diagnosticWCLProcessInfo{}) - 668]byte

type diagnosticWCLOwnedProcess struct {
	identity diagnosticWCLIdentity
	handle   windows.Handle
}

// This state belongs only to fixture(t). Its root cleanup releases every handle
// after the original RemoveAll result, including successful and skipped probes.
type diagnosticWCLObserver struct {
	enabled bool
	owned   []diagnosticWCLOwnedProcess
}

func (o *diagnosticWCLObserver) enable() {
	o.enabled = os.Getenv("SIDERA_CI_DIAGNOSTIC_WCL_OBSERVER") == "1"
}

func diagnosticWCLOpen(pid uint32) (diagnosticWCLOwnedProcess, string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return diagnosticWCLOwnedProcess{}, "open_process_failed", err
	}
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		_ = windows.CloseHandle(h)
		return diagnosticWCLOwnedProcess{}, "process_times_failed", err
	}
	return diagnosticWCLOwnedProcess{
		identity: diagnosticWCLIdentity{pid, diagnosticWCLFiletime{created.LowDateTime, created.HighDateTime}},
		handle:   h,
	}, "identity_captured", nil
}

func (o *diagnosticWCLObserver) capture(hello fakectl.HelloParams, instance string) {
	if !o.enabled {
		return
	}
	// The connection was obtained by the existing Wait and synchronization. A
	// role/name mismatch supplies no owned adapter identity; filenames never do.
	if hello.Role != fakectl.RoleAdapter || hello.ID != instance || hello.PID <= 0 || hello.PID > int64(^uint32(0)) {
		diagnosticWCLEmit(diagnosticWCLRecord{Phase: "identity", Status: "hello_identity_unavailable"})
		return
	}
	pid := uint32(hello.PID)
	for _, p := range o.owned {
		if p.identity.PID == pid {
			return
		}
	}
	start := time.Now()
	p, status, err := diagnosticWCLOpen(pid)
	r := diagnosticWCLRecord{Phase: "identity", Status: status, Errno: diagnosticWCLNativeErrno(err), DurationMS: time.Since(start).Milliseconds()}
	if err == nil {
		o.owned = append(o.owned, p)
		r.Identity = &p.identity
	}
	diagnosticWCLEmit(r)
}

func (o *diagnosticWCLObserver) afterRemove(root string, removeErr error) {
	if !o.enabled {
		return
	}
	defer func() {
		for _, p := range o.owned {
			if err := windows.CloseHandle(p.handle); err != nil {
				diagnosticWCLEmit(diagnosticWCLRecord{Phase: "release", Status: "close_handle_failed", Errno: diagnosticWCLNativeErrno(err), Identity: &p.identity})
			}
		}
		o.owned = nil
	}()
	if removeErr == nil {
		return
	}
	path, rel, ok := diagnosticWCLFailedPath(root, removeErr)
	if !ok {
		diagnosticWCLEmit(diagnosticWCLRecord{Phase: "after_remove", Status: "remove_path_not_adapter_ledger"})
		return
	}
	first := diagnosticWCLQuery(path, rel, "after_remove")
	diagnosticWCLEmit(first)
	if first.Status != "query_success" {
		return
	}
	var matches []diagnosticWCLOwnedProcess
	for _, owned := range o.owned {
		for _, user := range first.ResourceUsers {
			if owned.identity == user {
				matches = append(matches, owned)
				break
			}
		}
	}
	if len(matches) != 1 {
		diagnosticWCLEmit(diagnosticWCLRecord{Phase: "wait", Status: "unique_owned_identity_unavailable", RelativePath: rel, OwnedMatches: len(matches)})
		return
	}
	// Exactly one pre-held owned handle, one wait, at most ten seconds total.
	// RM-only identities are never opened, waited for or otherwise operated on.
	start := time.Now()
	status, err := windows.WaitForSingleObject(matches[0].handle, 10_000)
	r := diagnosticWCLRecord{Phase: "wait", Status: "wait_unexpected_status", RelativePath: rel, Identity: &matches[0].identity, OwnedMatches: 1, WaitStatus: &status, Errno: diagnosticWCLNativeErrno(err), DurationMS: time.Since(start).Milliseconds()}
	switch {
	case err != nil:
		r.Status = "wait_failed"
	case status == windows.WAIT_OBJECT_0:
		r.Status = "owned_process_exited"
	case status == uint32(windows.WAIT_TIMEOUT):
		r.Status = "owned_process_not_exited_within_bound"
	}
	diagnosticWCLEmit(r)
	diagnosticWCLEmit(diagnosticWCLQuery(path, rel, "after_wait"))
}

var diagnosticWCLRM = windows.NewLazySystemDLL("rstrtmgr.dll")
var diagnosticWCLStart = diagnosticWCLRM.NewProc("RmStartSession")
var diagnosticWCLRegister = diagnosticWCLRM.NewProc("RmRegisterResources")
var diagnosticWCLGetList = diagnosticWCLRM.NewProc("RmGetList")
var diagnosticWCLEnd = diagnosticWCLRM.NewProc("RmEndSession")

// Restart Manager may write its internal registry state. The four API calls have
// no independent deadline; only the outer Go timeout bounds a stuck API call.
func diagnosticWCLQuery(path, rel, phase string) (r diagnosticWCLRecord) {
	start := time.Now()
	r = diagnosticWCLRecord{Phase: phase, RelativePath: rel}
	defer func() { r.DurationMS = time.Since(start).Milliseconds() }()
	info, err := os.Lstat(path)
	if err != nil {
		r.Status = "path_stat_failed"
		if os.IsNotExist(err) {
			r.Status = "path_missing_after_remove_error"
		}
		r.Errno = diagnosticWCLNativeErrno(err)
		return r
	}
	if !info.Mode().IsRegular() {
		r.Status = "path_not_regular_file"
		return r
	}
	for _, proc := range []*windows.LazyProc{diagnosticWCLStart, diagnosticWCLRegister, diagnosticWCLGetList, diagnosticWCLEnd} {
		if err := proc.Find(); err != nil {
			r.Status, r.Errno = "api_unavailable", diagnosticWCLNativeErrno(err)
			return r
		}
	}
	var session uint32
	var key [33]uint16
	code, _, _ := diagnosticWCLStart.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0])))
	if code != 0 {
		r.Status, r.Errno = "session_start_failed", uint32(code)
		return r
	}
	defer func() {
		code, _, _ := diagnosticWCLEnd.Call(uintptr(session))
		r.EndErrno = uint32(code)
	}()
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		r.Status, r.Errno = "resource_path_invalid", diagnosticWCLNativeErrno(err)
		return r
	}
	// Register exactly one file, zero processes and zero services.
	code, _, _ = diagnosticWCLRegister.Call(uintptr(session), 1, uintptr(unsafe.Pointer(&file)), 0, 0, 0, 0)
	if code != 0 {
		r.Status, r.Errno = "resource_register_failed", uint32(code)
		return r
	}
	var needed, count, reasons uint32
	code, _, _ = diagnosticWCLGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)), 0, uintptr(unsafe.Pointer(&reasons)))
	r.Needed = needed
	if code == 0 && count == 0 {
		r.Status = "query_success_empty"
		return r
	}
	if code != uintptr(windows.ERROR_MORE_DATA) || needed == 0 {
		r.Status, r.Errno = "list_count_failed", uint32(code)
		return r
	}
	// One allocation and one read. A changing population remains incomplete.
	apps := make([]diagnosticWCLProcessInfo, needed)
	count = needed
	code, _, _ = diagnosticWCLGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&apps[0])), uintptr(unsafe.Pointer(&reasons)))
	r.Needed, r.Returned = needed, count
	if code != 0 {
		r.Status, r.Errno = "list_read_failed", uint32(code)
		if code == uintptr(windows.ERROR_MORE_DATA) {
			r.Status = "list_incomplete_population_changed"
		}
		return r
	}
	if count > uint32(len(apps)) {
		r.Status = "list_incomplete_count_invalid"
		return r
	}
	r.ResourceUsers = diagnosticWCLUsers(apps[:count])
	r.Status = "query_success"
	if count == 0 {
		r.Status = "query_success_empty"
	}
	return r
}
