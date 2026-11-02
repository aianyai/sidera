// Command sidera-adapter-claude is the Sidera adapter for Claude Code: it
// serves the adapter protocol on its standard input and output, and creates and
// drives Claude Code sessions as `claude -p` processes that outlive it. The
// daemon starts it from its package (sidera-adapter.json) and ends it by closing
// its input; standard error carries its log, which holds no prompt and no token.
//
// With the subcommand package it instead writes the adapter package for a
// platform from a binary built for it, and with version it prints its version.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// manifestTemplate is the manifest of the package, built into the executable so
// that the identity answered in initialize is read from it and cannot differ. Its
// version is the placeholder versionToken: the version is the release's, which
// loadManifest puts in.
//
//go:embed sidera-adapter.json
var manifestTemplate []byte

const versionToken = "@version@"

// loadManifest is the manifest built into the executable, with the release version.
func loadManifest() (manifest.Manifest, error) {
	raw := bytes.Replace(manifestTemplate, []byte(versionToken), []byte(release.Version()), 1)
	m, err := manifest.Parse(raw)
	if err != nil {
		return manifest.Manifest{}, fmt.Errorf("the manifest built into the executable: %w", err)
	}
	return m, nil
}

func main() {
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "package":
		err = packageCommand(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "version":
		fmt.Println(release.Read().Describe("sidera-adapter-claude"))
	default:
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
	m, err := loadManifest()
	if err != nil {
		return adapterkit.Config{}, err
	}
	return adapterkit.Config{
		Adapter: adapterproto.AdapterInfo{ID: m.ID, Version: m.Version},
		Capabilities: adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{
			// Interrupt and acknowledging on the agent's behalf wait for their own work, and
			// `claude -p` sends the adapter no requests that need an answer.
			Interrupt: false, AckOnBehalf: false, ServerRequests: false,
			Frontends: []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP},
			// A session a person opened can be attached: woken through its inbox socket and
			// followed through its registry entry and its transcript.
			Attach: true,
		}},
		App: app, Logger: logger,
	}, nil
}
