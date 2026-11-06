// Command sidera-adapter-claude is the Sidera adapter for Claude Code: it
// serves the adapter protocol on its standard input and output, and creates and
// drives Claude Code sessions as `claude -p` processes that outlive it. The
// daemon starts it from its package (sidera-adapter.json) and ends it by closing
// its input; standard error carries its log, which holds no prompt and no token.
//
// It does nothing unless it is told what: the subcommand serve (what the manifest's
// entry says) serves, package writes the adapter package for a platform from a
// binary built for it, setup is the client setup `sidera adapter setup` runs, and
// version prints its version. See usageText.
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
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/setup"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
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

const programName = "sidera-adapter-claude"

// loadManifest is the manifest built into the executable, with the release version.
func loadManifest() (manifest.Manifest, error) {
	raw := bytes.Replace(manifestTemplate, []byte(versionToken), []byte(release.Version()), 1)
	m, err := manifest.Parse(raw)
	if err != nil {
		return manifest.Manifest{}, fmt.Errorf("the manifest built into the executable: %w", err)
	}
	return m, nil
}

const usageText = `usage: sidera-adapter-claude <command>

commands:
  serve      the adapter role; the daemon starts it, a person does not
  package --goos <os> --goarch <arch> --binary <file> --out <directory>
             write an adapter package for 'sidera adapter install'
  setup      bring Claude Code's configuration in line with an instance; run by
             'sidera adapter setup', not by a person
  version [--json]
             the version, the commit and the Go version of this program
`

// program is the command line of the executable.
var program = adapterkit.Program{
	Name:    programName,
	Usage:   usageText,
	Serve:   serve,
	Package: writePackage,
	Build:   release.Read(),
	Commands: map[string]adapterkit.Command{
		"setup": adaptersetup.Command(programName, setup.New(setup.Options{Version: release.Version(), Environ: os.Environ(), Home: userHome()})),
	},
}

// userHome is the home directory of the person running the program, where Claude Code's
// default configuration is; empty when it is not known.
func userHome() string {
	home, _ := os.UserHomeDir()
	return home
}

func main() {
	// A helper process of the SDK's interrupt (Windows) is this executable started
	// with an argument of its own; it does its work and ends here.
	process.HandleHelperInvocation()
	os.Exit(program.Run(os.Args[1:], adapterkit.Streams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}))
}

// serve is the command the manifest's entry names.
func serve(s adapterkit.Streams) int {
	if err := run(); err != nil {
		fmt.Fprintln(s.Stderr, programName+":", err)
		return adapterkit.ExitFailed
	}
	return adapterkit.ExitOK
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
			// A turn is interrupted like Ctrl-C does it. Acknowledging on the agent's
			// behalf waits for its own work, and `claude -p` sends the adapter no
			// requests that need an answer.
			Interrupt: true, AckOnBehalf: false, ServerRequests: false,
			Frontends: []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP},
			// A session a person opened can be attached: woken through its inbox socket and
			// followed through its registry entry and its transcript.
			Attach: true,
		}},
		App: app, Logger: logger,
	}, nil
}
