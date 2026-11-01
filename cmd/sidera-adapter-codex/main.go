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
	"flag"
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
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "package":
		err = packageCommand(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "version":
		fmt.Println(release.Read().Describe("sidera-adapter-codex"))
	default:
		err = serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sidera-adapter-codex:", err)
		os.Exit(1)
	}
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

func packageCommand(args []string) error {
	fs := flag.NewFlagSet("package", flag.ContinueOnError)
	goos := fs.String("goos", "", "operating system the binary was built for (GOOS)")
	goarch := fs.String("goarch", "", "architecture the binary was built for (GOARCH)")
	binary := fs.String("binary", "", "the binary built for that platform")
	out := fs.String("out", "", "the directory to write the package into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *goos == "" || *goarch == "" || *binary == "" || *out == "" {
		return errors.New("package needs --goos, --goarch, --binary and --out")
	}
	return declare.WritePackage(*out, *binary, *goos, *goarch)
}
