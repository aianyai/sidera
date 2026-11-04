package adapter

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// ProgramEnv is what the program is given by its process.
type ProgramEnv struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

const usageText = `usage: sidera-adapter-hermes <command>

commands:
  serve      the adapter role; the daemon starts it, a person does not
  package --goos <os> --goarch <arch> --binary <file> --out <directory>
             write an adapter package for 'sidera adapter install'
  version    the version, the commit and the Go version of this program
`

const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// Main runs the program with its arguments after the program name and returns the
// exit code.
func Main(args []string, env ProgramEnv) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(env.Stderr, usageText)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	switch args[0] {
	case "serve":
		return runServe(env)
	case "package":
		return runPackage(args[1:], env)
	case "version":
		fmt.Fprintln(env.Stdout, release.Read().Describe(ProgramName))
		return exitOK
	}
	fmt.Fprintf(env.Stderr, "sidera-adapter-hermes: unknown command %q\n%s", args[0], usageText)
	return exitUsage
}

func runServe(env ProgramEnv) int {
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out, ok := env.Stdout.(io.WriteCloser)
	if !ok {
		out = nopCloser{env.Stdout}
	}
	if err := Serve(ctx, env.Stdin, out, log, LaneFlows(NewInbound)); err != nil {
		fmt.Fprintln(env.Stderr, "sidera-adapter-hermes:", err)
		return exitFailed
	}
	return exitOK
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func runPackage(args []string, env ProgramEnv) int {
	fs := flag.NewFlagSet("package", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var goos, goarch, binary, out string
	fs.StringVar(&goos, "goos", "", "")
	fs.StringVar(&goarch, "goarch", "", "")
	fs.StringVar(&binary, "binary", "", "")
	fs.StringVar(&out, "out", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || goos == "" || goarch == "" || binary == "" || out == "" {
		fmt.Fprintln(env.Stderr, "sidera-adapter-hermes: package needs --goos, --goarch, --binary and --out")
		return exitUsage
	}
	if err := writePackage(goos, goarch, binary, out); err != nil {
		fmt.Fprintln(env.Stderr, "sidera-adapter-hermes:", err)
		return exitFailed
	}
	fmt.Fprintln(env.Stdout, "package:", out)
	return exitOK
}

func writePackage(goos, goarch, binary, out string) error {
	m, err := Manifest(goos, goarch)
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty", out)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return manifest.WritePackage(out, m, binary)
}
