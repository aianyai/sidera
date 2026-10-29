//go:build crashtest

package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	randv2 "math/rand/v2"
	"os"
	"strconv"
	"sync"

	"github.com/aianyai/sidera/internal/node/daemon"
)

// lockedRandom meets daemon.Config's concurrent-reader contract while keeping
// one deterministic stream for all of that daemon's consumers.
type lockedRandom struct {
	mu     sync.Mutex
	source io.Reader
}

func (r *lockedRandom) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.source.Read(p)
}

func (c *command) configureTestProcess(role string) (func() error, error) {
	// These controls belong to this daemon generation. Read them together before
	// removing them from the environment inherited by adapters and native tools.
	record, target, generation := c.Getenv("SIDERA_CRASH_RECORD"), c.Getenv("SIDERA_CRASH_AT"), c.Getenv("SIDERA_CRASH_GEN")
	if role == "daemon" {
		for _, name := range []string{"SIDERA_CRASH_RECORD", "SIDERA_CRASH_AT", "SIDERA_CRASH_GEN"} {
			if err := os.Unsetenv(name); err != nil {
				return nil, err
			}
		}
	}
	gen := 1
	if value := generation; value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return nil, fmt.Errorf("SIDERA_CRASH_GEN must be a positive integer")
		}
		gen = parsed
	}
	c.random = rand.Reader
	if seed := c.Getenv("SIDERA_TEST_SEED"); seed != "" {
		domain := "sidera-test-random-v1\x00" + seed + "\x00" + role + "\x00" + strconv.Itoa(gen)
		source := randv2.NewChaCha8(sha256.Sum256([]byte(domain)))
		c.random = &lockedRandom{source: source}
	}
	if record == "" {
		if target != "" {
			return nil, fmt.Errorf("SIDERA_CRASH_AT requires SIDERA_CRASH_RECORD")
		}
		return func() error { return nil }, nil
	}
	file, err := os.OpenFile(record, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	observer, err := daemon.NewCrashObserver(daemon.CrashConfig{Writer: file, At: target, Gen: gen})
	if err != nil {
		file.Close()
		return nil, err
	}
	c.observer = observer
	c.afterRecv = observer.RecvResponse
	return file.Close, nil
}
