// Command sidera-adapter-codex is the adapter that lets the daemon create and
// drive Codex sessions. The daemon starts it and speaks the adapter protocol with
// it over standard input and output; standard error is its log.
//
// It does nothing unless it is told what: the subcommand serve (what the manifest's
// entry says) serves, package writes the adapter package for a platform from a
// binary built for it, and version prints its version. See usageText.
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

const programName = "sidera-adapter-codex"

const usageText = `usage: sidera-adapter-codex <command>

commands:
  serve      the adapter role; the daemon starts it, a person does not
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
