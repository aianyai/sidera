//go:build diagnostic

package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// These layouts mirror FILETIME, RM_UNIQUE_PROCESS and RM_PROCESS_INFO. Raw
// Restart Manager names remain here and never enter the console projection.
type diagnosticWCLFiletime struct {
	Low  uint32 `json:"low"`
	High uint32 `json:"high"`
}

type diagnosticWCLIdentity struct {
	PID     uint32                `json:"pid"`
	Created diagnosticWCLFiletime `json:"creation_filetime"`
}

type diagnosticWCLProcessInfo struct {
	Process         diagnosticWCLIdentity
	AppName         [256]uint16
	ServiceName     [64]uint16
	ApplicationType uint32
	AppStatus       uint32
	TSSessionID     uint32
	Restartable     int32
}

// All string fields other than RelativePath are fixed literals at call sites.
// Queries are later, non-atomic resource-user samples, never handle ownership.
type diagnosticWCLRecord struct {
	Phase         string                  `json:"phase"`
	Status        string                  `json:"status"`
	RelativePath  string                  `json:"relative_path,omitempty"`
	Errno         uint32                  `json:"errno"`
	EndErrno      uint32                  `json:"end_errno,omitempty"`
	DurationMS    int64                   `json:"duration_ms"`
	Needed        uint32                  `json:"needed,omitempty"`
	Returned      uint32                  `json:"returned,omitempty"`
	ResourceUsers []diagnosticWCLIdentity `json:"resource_users,omitempty"`
	Identity      *diagnosticWCLIdentity  `json:"identity,omitempty"`
	OwnedMatches  int                     `json:"owned_matches,omitempty"`
	WaitStatus    *uint32                 `json:"wait_status,omitempty"`
}

func diagnosticWCLUsers(raw []diagnosticWCLProcessInfo) []diagnosticWCLIdentity {
	users := make([]diagnosticWCLIdentity, len(raw))
	for i := range raw {
		users[i] = raw[i].Process
	}
	return users
}

func diagnosticWCLNativeErrno(err error) uint32 {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return uint32(errno)
	}
	return 0
}

func diagnosticWCLWrite(w io.Writer, record diagnosticWCLRecord) {
	// The concrete record has no raw API result or error string to serialize.
	raw, err := json.Marshal(record)
	if err != nil {
		panic("diagnostic wcl projection cannot encode")
	}
	fmt.Fprintf(w, "[DEBUG-wcl] %s\n", raw)
}

func diagnosticWCLEmit(record diagnosticWCLRecord) { diagnosticWCLWrite(os.Stdout, record) }

var diagnosticWCLLedgerName = regexp.MustCompile(`^adapter_[A-Za-z0-9._:@-]+-[0-9]+\.jsonl$`)

func diagnosticWCLFailedPath(root string, removeErr error) (string, string, bool) {
	var pe *os.PathError
	if !errors.As(removeErr, &pe) || !filepath.IsAbs(pe.Path) {
		return "", "", false
	}
	path := filepath.Clean(pe.Path)
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.Dir(rel) != "l" || !diagnosticWCLLedgerName.MatchString(filepath.Base(rel)) {
		return "", "", false
	}
	return path, filepath.ToSlash(rel), true
}
