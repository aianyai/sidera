package keys_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/recfile"
)

// readCounter counts the files read through it.
type readCounter struct {
	fsx.Reader
	reads int
}

func (c *readCounter) ReadFile(p string) ([]byte, error) {
	c.reads++
	return c.Reader.ReadFile(p)
}

func (c *readCounter) ReadFileLimit(p string, maxBytes int64) ([]byte, error) {
	c.reads++
	return c.Reader.ReadFileLimit(p, maxBytes)
}

// A file of keys/ is no larger than the largest record file, so one that is
// larger is damaged, and it is refused by its size, before it is read into
// memory: a file the owner or a full disk left there must not be able to make a
// start allocate without bound.
func TestAFileOverTheLargestRecordIsRefusedWithoutBeingRead(t *testing.T) {
	huge := []byte(strings.Repeat("x", recfile.MaxHeaderLen+recfile.MaxBodyLen+1))
	read := map[string]func(fsx.Reader) error{
		keys.ClockPath:          func(r fsx.Reader) error { _, err := keys.ReadClock(r); return err },
		keys.SelfRevocationPath: func(r fsx.Reader) error { _, err := keys.ReadSelfRevocation(r); return err },
		keys.InitMarkerPath:     func(r fsx.Reader) error { _, err := keys.Initialized(r); return err },
	}
	for p, call := range read {
		t.Run(p, func(t *testing.T) {
			r := newRig(t)
			r.mustInit(1, t0)
			put(t, r.mem, p, huge)
			counter := &readCounter{Reader: r.mem}
			err := call(counter)
			if !errors.Is(err, keys.ErrCorrupt) {
				t.Fatalf("a file of %d bytes: %v, want ErrCorrupt", len(huge), err)
			}
			asFileError(t, err, p)
			if counter.reads != 0 {
				t.Errorf("the file was read %d times", counter.reads)
			}
		})
	}
}
