package main

import (
	"context"
	"os"
	"runtime/debug"

	"github.com/aianyai/sidera/cmd/sidera/internal/cli"
)

func main() {
	version := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		version = info.Main.Version
	}
	os.Exit(cli.Run(context.Background(), os.Args, cli.Config{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		Version: version,
	}))
}
