// Package logfile is how the daemon and its adapter hosts write their logs: a
// restricted handle on the file system that can write under logs/ and nowhere
// else, and the rotation of the files it writes. Logs are diagnostic, so they do
// not go through the transaction layer and are not crash points; see README.md.
package logfile

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sync"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Dir is the directory of the logs, relative to the data directory.
const Dir = "logs"

// MinThreshold is the smallest size at which a log is rotated: 1 MiB.
const MinThreshold int64 = 1 << 20

// rotatedSuffix is added to the name of a log to name its one rotation.
const rotatedSuffix = ".1"

// tag is the tag of every step of a log: the reserved transaction that the
// numbering of crash points skips.
var tag = fsx.Tag{Tx: fsx.TxLog, Role: "file"}

// nameRE is what the name of a log looks like: the daemon's own, or one of an
// adapter instance, whose name is an adapter identifier. It holds no separator,
// no dot but the one of the extension and nothing hidden, so a name can lead
// nowhere but to a file directly under logs/.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}\.log$`)

// Threshold is the size at which each log is rotated: the total the logs may take
// shared among two files (the log and its rotation) for the daemon and for every
// enabled instance, never below MinThreshold. The floor lets an extreme number of
// instances take a little more than the total: that only happens beyond the
// number of endpoints a node is sized for, and the status reports the real size.
func Threshold(total int64, instances int) int64 {
	if instances < 0 {
		instances = 0
	}
	t := total / (2 * int64(1+instances))
	return max(t, MinThreshold)
}

// Handle writes logs through a file system. It is built from the writable file
// system and offers no operation but the opening, rotation and removal of logs,
// so what goes through it cannot touch state, configuration or keys.
type Handle struct{ fs fsx.FS }

// New returns the handle over fs. logs/ exists: init makes it, and the start
// makes it again if the owner removed it.
func New(fs fsx.FS) *Handle { return &Handle{fs: fs} }

// ErrBadName is returned for a name that is not the name of a log.
var ErrBadName = errors.New("logfile: not the name of a log")

func checkName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrBadName, name)
	}
	return nil
}

// Open opens the log called name for appending, creating it when it is not there.
// limit is the size at which the log is rotated; it is asked at every write, so a
// change of the number of instances takes effect at the next rotation without
// reopening the log.
func (h *Handle) Open(name string, limit func() int64) (*Log, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	if limit == nil {
		return nil, errors.New("logfile: a log needs a threshold")
	}
	l := &Log{h: h, p: path.Join(Dir, name), limit: limit}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

// Remove deletes the log called name and its rotation, as when the instance it
// belongs to is disabled. A file that is not there is no error.
func (h *Handle) Remove(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	p := path.Join(Dir, name)
	for _, f := range []string{p, p + rotatedSuffix} {
		if err := h.fs.Remove(tag, f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Log is an open log. It is safe for concurrent use; every Write is one line, and
// a line goes whole to one file.
type Log struct {
	h     *Handle
	p     string
	limit func() int64

	mu   sync.Mutex
	f    fsx.File
	size int64
}

// open opens the file at the end and learns the size it already has, so that a log
// that is opened again goes on towards the same threshold.
func (l *Log) open() error {
	var size int64
	switch info, err := l.h.fs.Stat(l.p); {
	case err == nil:
		size = info.Size
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	f, err := l.h.fs.OpenAppend(tag, l.p)
	if err != nil {
		return err
	}
	l.f, l.size = f, size
	return nil
}

// Write appends p, one line. When the file already holds something and the line
// would take it past the threshold, the file becomes the rotation, replacing the
// older one, and the line starts a new file; a line longer than the threshold is
// written whole to a file of its own. A failed write is returned and keeps nothing
// of the line; the next write goes on.
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, errors.New("logfile: the log is closed")
	}
	if l.size > 0 && l.size+int64(len(p)) > l.limit() {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// rotate makes the full file the rotation and opens a new one. If the rename
// fails the file is opened again as it was and the error is returned, so the line
// is refused and the next write tries the rotation again: a failing disk does not
// leave the log without a file.
func (l *Log) rotate() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	l.f = nil
	if err := l.h.fs.Rename(tag, l.p, l.p+rotatedSuffix); err != nil {
		// The file is whole; open it again.
		if oerr := l.open(); oerr != nil {
			return errors.Join(err, oerr)
		}
		return err
	}
	return l.open()
}

// Close closes the log. It is safe to call twice.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
