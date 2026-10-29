//go:build crashtest

package cli

import (
	"bytes"
	"io"
	"strconv"
	"sync"
	"testing"
)

func TestDeterministicRandomDomains(t *testing.T) {
	sample := func(role, seed string, gen int) []byte {
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
		data := make([]byte, 64)
		if _, err := io.ReadFull(c.random, data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	base := sample("daemon", "seed/call1", 1)
	if !bytes.Equal(base, sample("daemon", "seed/call1", 1)) {
		t.Fatal("same domain changed")
	}
	for _, other := range [][]byte{sample("init", "seed/call1", 1), sample("cli", "seed/call1", 1), sample("daemon", "seed/call1", 2), sample("daemon", "seed/call2", 1)} {
		if bytes.Equal(base, other) {
			t.Fatal("role, generation or call domains overlapped")
		}
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
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = io.ReadFull(c.random, make([]byte, 32)) }()
	}
	wg.Wait()
}
