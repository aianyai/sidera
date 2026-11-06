// Command sidera-adapter-codex is the adapter that lets the daemon create and
// drive Codex sessions. The daemon starts it and speaks the adapter protocol with
// it over standard input and output; standard error is its log.
//
// With the subcommand package it instead writes the adapter package for a
// platform from a binary built for it, and with version it prints its version.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/adapter"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/declare"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
)

func main() {
	streams := adapterkit.Streams{Stdout: os.Stdout, Stderr: os.Stderr}
	switch {
	case len(os.Args) > 1 && os.Args[1] == "package":
		os.Exit(adapterkit.RunPackage(programName, os.Args[2:], declare.WritePackage, streams))
	case len(os.Args) > 1 && os.Args[1] == "version":
		os.Exit(adapterkit.RunVersion(programName, os.Args[2:], release.Read(), streams))
	}
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, programName+":", err)
		os.Exit(adapterkit.ExitFailed)
	}
}

const programName = "sidera-adapter-codex"

func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	err := adapterkit.ServeStdio(ctx, adapterkit.Config{
		Adapter:      declare.Info(),
		Capabilities: declare.Capabilities(),
		App:          adapter.New(adapter.Options{Logger: logger}),
		Logger:       logger,
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
