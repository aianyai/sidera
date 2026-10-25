// Command sidera-fake-runtime is the fake native runtime as a program of its own:
// one process per session, started detached by the fake adapter, which outlives
// the adapter so that a restarted adapter can take its session back. It is a test
// tool and is never installed.
//
// It reads a fakeruntime.ProcessSpec, as JSON, from the environment variable
// SIDERA_FAKE_RUNTIME_SPEC, creates the session, and prints one line of JSON on
// standard output when the session is ready: the fakeruntime.ProcessInfo that says
// where to reach it. It ends when its control connection ends or when its native
// process is killed.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sidera-fake-runtime:", err)
		os.Exit(1)
	}
}

func run() error {
	spec, err := fakeruntime.DecodeProcessSpec([]byte(os.Getenv(fakeruntime.SpecEnv)))
	if err != nil {
		return fmt.Errorf("%s: %w", fakeruntime.SpecEnv, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return fakeruntime.RunProcess(ctx, spec, &fakeruntime.ExecRunner{Seed: spec.TestSeed}, func(info fakeruntime.ProcessInfo) {
		line, _ := json.Marshal(info)
		fmt.Fprintf(os.Stdout, "%s\n", line)
	})
}
