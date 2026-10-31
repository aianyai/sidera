// Command sidera-adapter-claude is the Sidera adapter for Claude Code: it
// serves the adapter protocol on its standard input and output, and creates and
// drives Claude Code sessions as `claude -p` processes that outlive it. The
// daemon starts it from its package (sidera-adapter.json) and ends it by closing
// its input; standard error carries its log, which holds no prompt and no token.
//
// With the subcommand package it instead writes the adapter package for a
// platform from a binary built for it.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// manifestJSON is the manifest of the package, built into the executable so that
// the identity answered in initialize is read from it and cannot differ.
//
//go:embed sidera-adapter.json
var manifestJSON []byte

func main() {
	var err error
	if len(os.Args) > 1 && os.Args[1] == "package" {
		err = packageCommand(os.Args[2:])
	} else {
		err = run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sidera-adapter-claude:", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config(runtime.New(runtime.Options{Logger: logger}), logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return adapterkit.ServeStdio(ctx, cfg)
}

// config returns what the adapter serves with: the identity from the manifest,
// the capabilities it declares and the application.
func config(app adapterkit.Application, logger *slog.Logger) (adapterkit.Config, error) {
	m, err := manifest.Parse(manifestJSON)
	if err != nil {
		return adapterkit.Config{}, fmt.Errorf("the manifest built into the executable: %w", err)
	}
	return adapterkit.Config{
		Adapter: adapterproto.AdapterInfo{ID: m.ID, Version: m.Version},
		Capabilities: adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{
			// Interrupt and acknowledging on the agent's behalf wait for their own work, and
			// `claude -p` sends the adapter no requests that need an answer.
			Interrupt: false, AckOnBehalf: false, ServerRequests: false,
			Frontends: []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP},
		}},
		App: app, Logger: logger,
	}, nil
}
