package daemon_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
)

// Observe the public Init and Start contracts, including the reader hooks receive.
func TestTemporaryRandomnessIsOwnedByTheConfiguredFilesystem(t *testing.T) {
	type result struct {
		node string
		next [32]byte
	}
	run := func(t *testing.T, mode string, extra int) result {
		t.Helper()
		semantic := seeded(31)
		temporary := &countingRandom{source: seeded(32)}
		clk := clocktest.NewFake(t0)
		cfg := daemon.Config{Home: newHome(t), Clock: clk, Rand: semantic}
		switch mode {
		case "shared":
			cfg.TempRand = semantic
		case "separate":
			cfg.TempRand = temporary
		case "filesystem":
			if err := os.Mkdir(cfg.Home, 0o700); err != nil {
				t.Fatal(err)
			}
			var err error
			cfg.FS, err = fsx.OpenOS(cfg.Home, temporary)
			if err != nil {
				t.Fatal(err)
			}
			// Any daemon replacement of the supplied FS reader fails visibly.
			cfg.TempRand = bytes.NewReader(nil)
		}
		if _, err := io.CopyN(io.Discard, temporary, int64(extra)); err != nil {
			t.Fatal(err)
		}
		report, err := daemon.Init(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		out := result{node: report.Node.String()}
		afterInit := temporary.count()
		// Start repairs missing layout and persists a newly detected clock rollback.
		clk.Set(t0.Add(-time.Hour))
		if err := os.RemoveAll(filepath.Join(cfg.Home, "state")); err != nil {
			t.Fatal(err)
		}
		cfg.Attach.AgentServe = func(_ context.Context, env *daemon.Env) error {
			_, err := io.ReadFull(env.Rand, out.next[:])
			return err
		}
		d, err := daemon.Start(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		d.RequestStop("randomness contract checked")
		if err := d.Wait(); err != nil {
			t.Fatal(err)
		}
		if mode == "separate" || mode == "filesystem" {
			if afterInit <= extra || temporary.count() <= afterInit {
				t.Fatal("Init or Start did not consume the filesystem's temporary-name reader")
			}
		}
		return out
	}
	t.Run("default shares Rand", func(t *testing.T) {
		if run(t, "default", 0) != run(t, "shared", 0) {
			t.Fatal("nil TempRand changed the shared Rand stream")
		}
	})
	t.Run("temporary draws do not advance Rand", func(t *testing.T) {
		if run(t, "separate", 0) != run(t, "separate", 80) {
			t.Fatal("temporary-name draws changed semantic randomness")
		}
	})
	t.Run("explicit FS retains its reader", func(t *testing.T) {
		if run(t, "filesystem", 0) != run(t, "separate", 0) {
			t.Fatal("explicit FS changed semantic randomness")
		}
	})
}

type countingRandom struct {
	mu     sync.Mutex
	source io.Reader
	n      int
}

func (r *countingRandom) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := r.source.Read(p)
	r.n += n
	return n, err
}

func (r *countingRandom) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}
