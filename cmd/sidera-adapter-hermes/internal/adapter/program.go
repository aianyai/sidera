package adapter

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/setup"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// ProgramEnv is what the program is given by its process.
type ProgramEnv struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Getenv reads the environment that decides how the output is decorated and colored.
	Getenv func(string) string
}

var help = adapterkit.Help{
	Usage: ProgramName + " <command>",
	Groups: []adapterkit.HelpGroup{{Title: "Client setup", Entries: []adapterkit.HelpEntry{
		{Name: "setup", Arguments: "[-- --profile <name> --platform <name>]",
			Purpose: "The client setup that `sidera adapter setup <instance>` runs, not a person: it reads a request on standard input."},
	}}},
}

const (
	exitOK     = adapterkit.ExitOK
	exitFailed = adapterkit.ExitFailed
)

// Main runs the program with its arguments after the program name and returns the
// exit code.
func Main(args []string, env ProgramEnv) int {
	return adapterkit.Program{
		Name:     ProgramName,
		Help:     help,
		Serve:    runServe,
		Package:  writePackage,
		Build:    release.Read(),
		Commands: map[string]adapterkit.Command{"setup": adaptersetup.Command(ProgramName, setup.NewHandler())},
	}.Run(args, adapterkit.Streams{Stdin: env.Stdin, Stdout: env.Stdout, Stderr: env.Stderr, Getenv: env.Getenv})
}

func runServe(s adapterkit.Streams) int {
	log := slog.New(slog.NewTextHandler(s.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out, ok := s.Stdout.(io.WriteCloser)
	if !ok {
		out = nopCloser{s.Stdout}
	}
	if err := Serve(ctx, s.Stdin, out, log, LaneFlows(NewInbound)); err != nil {
		fmt.Fprintln(s.Stderr, "sidera-adapter-hermes:", err)
		return exitFailed
	}
	return exitOK
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func writePackage(goos, goarch, binary, out string) error {
	m, err := Manifest(goos, goarch)
	if err != nil {
		return err
	}
	return manifest.WritePackage(out, m, binary)
}
