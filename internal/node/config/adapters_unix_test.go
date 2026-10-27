//go:build unix

package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
)

func TestStagingRefusesAFIFOAsAnUnreadableSourceAndLeavesNothing(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "direct"
		if linked {
			name = "linked"
		}
		t.Run(name, func(t *testing.T) {
			rg, c := loaded(t)
			before := snapshot(t, rg.mem)
			src, root := osPackage(t)
			fifo := filepath.Join(root, "payload")
			if linked {
				fifo = filepath.Join(t.TempDir(), "pipe")
			}
			if err := unix.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			if linked {
				if err := os.Symlink(fifo, filepath.Join(root, "payload")); err != nil {
					t.Fatal(err)
				}
			}
			_, err := c.StageAdapter(src, suffixReader())
			var source *config.SourceError
			if !errors.As(err, &source) || source.Path != "payload" || !errors.Is(err, fsx.ErrNotRegular) {
				t.Fatalf("StageAdapter = %v, want SourceError for payload", err)
			}
			if after := snapshot(t, rg.mem); !reflect.DeepEqual(before, after) {
				t.Errorf("refused stage left %v", diffKeys(before, after))
			}
		})
	}
}
