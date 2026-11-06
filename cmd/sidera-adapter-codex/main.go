// Command sidera-adapter-codex is the adapter that lets the daemon create and
// drive Codex sessions. The daemon starts it and speaks the adapter protocol with
// it over standard input and output; standard error is its log.
//
// It does nothing unless it is told what: the subcommand serve (what the manifest's
// entry says) serves, setup brings Codex's own configuration in line with an instance
// (it is run by `sidera adapter setup`, not by a person), package writes the adapter
// package for a platform from a binary built for it, and version prints its version.
// See usageText.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/adapter"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/clientsetup"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/declare"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

const programName = "sidera-adapter-codex"

const usageText = `usage: sidera-adapter-codex <command>

commands:
  serve      the adapter role; the daemon starts it, a person does not
  setup      the client setup that 'sidera adapter setup' runs; it reads a request on
             standard input and writes a result on standard output
  package --goos <os> --goarch <arch> --binary <file> --out <directory>
             write an adapter package for 'sidera adapter install'
  version [--json]
             the version, the commit and the Go version of this program
`

// program is the command line of the executable.
var program = adapterkit.Program{
	Name:    programName,
	Usage:   usageText,
	Serve:   runServe,
	Package: declare.WritePackage,
	Build:   release.Read(),
	Commands: map[string]adapterkit.Command{
		"setup": adaptersetup.Command(programName, clientsetup.New(release.Version())),
	},
}

func main() {
	os.Exit(program.Run(os.Args[1:], adapterkit.Streams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}))
}

// runServe is the command the manifest's entry names.
func runServe(s adapterkit.Streams) int {
	if err := serve(); err != nil {
		fmt.Fprintln(s.Stderr, programName+":", err)
		return adapterkit.ExitFailed
	}
	return adapterkit.ExitOK
}

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
