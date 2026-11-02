package main

import (
	"context"
	"os"

	"github.com/aianyai/sidera/cmd/sidera/internal/cli"
	"github.com/aianyai/sidera/internal/release"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args, cli.Config{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		Executable: os.Executable,
		Release:    release.Read(),
	}))
}
