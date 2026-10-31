//go:build crashtest

package cli

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"io"
	randv2 "math/rand/v2"
	"strconv"
	"sync"
	"testing"
)

func TestDeterministicRandomDomains(t *testing.T) {
	sample := func(role, seed string, gen int, temporary bool, extra int) []byte {
		c := command{Config: Config{Getenv: func(name string) string {
			switch name {
			case "SIDERA_TEST_SEED":
				return seed
			case "SIDERA_CRASH_GEN":
				return strconv.Itoa(gen)
			}
			return ""
		}}}
		close, err := c.configureTestProcess(role)
		if err != nil {
			t.Fatal(err)
		}
		defer close()
		if _, err := io.CopyN(io.Discard, c.tempRandom, int64(extra)); err != nil {
			t.Fatal(err)
		}
		reader := c.random
		if temporary {
			reader = c.tempRandom
		}
		data := make([]byte, 64)
		if _, err := io.ReadFull(reader, data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, temporary := range []bool{false, true} {
		base := sample("daemon", "seed/call1", 1, temporary, 0)
		if !bytes.Equal(base, sample("daemon", "seed/call1", 1, temporary, 0)) {
			t.Fatal("same domain changed")
		}
		for _, other := range [][]byte{
			sample("init", "seed/call1", 1, temporary, 0),
			sample("cli", "seed/call1", 1, temporary, 0),
			sample("daemon", "seed/call1", 2, temporary, 0),
			sample("daemon", "seed/call2", 1, temporary, 0),
			sample("daemon", "seed/call1", 1, !temporary, 0),
		} {
			if bytes.Equal(base, other) {
				t.Fatal("purpose, role, generation or call domains overlapped")
			}
		}
	}
	base := sample("daemon", "seed/call1", 1, false, 0)
	if !bytes.Equal(base, sample("daemon", "seed/call1", 1, false, 80)) {
		t.Fatal("temporary draws advanced the semantic stream")
	}
	// Preserve the established semantic seed derivation, independent of temp draws.
	previous := randv2.NewChaCha8(sha256.Sum256([]byte("sidera-test-random-v1\x00seed/call1\x00daemon\x001")))
	want := make([]byte, len(base))
	if _, err := io.ReadFull(previous, want); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base, want) {
		t.Fatal("semantic seed derivation changed")
	}
}

func TestDaemonDeterministicRandomReaderCanBeShared(t *testing.T) {
	c := command{Config: Config{Getenv: func(name string) string {
		if name == "SIDERA_TEST_SEED" {
			return "shared"
		}
		return ""
	}}}
	close, err := c.configureTestProcess("daemon")
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	var wg sync.WaitGroup
	for range 20 {
		for _, reader := range []io.Reader{c.random, c.tempRandom} {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = io.ReadFull(reader, make([]byte, 32)) }()
		}
	}
	wg.Wait()
}

func TestEmptySeedRetainsSystemRandomness(t *testing.T) {
	for _, role := range []string{"init", "daemon", "cli"} {
		c := command{Config: Config{Getenv: func(string) string { return "" }}}
		close, err := c.configureTestProcess(role)
		if err != nil {
			t.Fatal(err)
		}
		defer close()
		if c.random != rand.Reader || c.tempRandom != nil {
			t.Fatal("empty seed installed deterministic randomness")
		}
	}
}
