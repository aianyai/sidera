// Command sidera-fake-adapter is the fake adapter as a program: the adapter
// protocol over standard input and output, a file ledger, native sessions that are
// runtime processes of their own, and the test clock, the running script step and
// its knobs under the control of the test fixture that started it. It is a test
// tool and is never installed.
//
// It reads a fakeadapter.ProcessConfig, as JSON, from the file named by the
// argument --config, or else from the environment variable
// SIDERA_FAKE_ADAPTER_CONFIG: several fake adapters under one daemon share an
// environment, and the entry arguments of their packages tell them apart. Standard
// error carries its log, which never holds message bodies. It ends when the daemon closes its input, or when the control
// connection to the fixture ends.
//
// Run as `setup`, it is the scripted setup of the package (fakeadapter.SetupCommand):
// one request on standard input, one result on standard output.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		os.Exit(fakeadapter.SetupCommand()(os.Args[2:], adapterkit.Streams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sidera-fake-adapter:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := fakeadapter.ReadProcessConfig(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return fakeadapter.RunProcess(ctx, cfg, os.Stdin, os.Stdout, logger)
}
