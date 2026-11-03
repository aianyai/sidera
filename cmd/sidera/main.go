package main

import (
	"context"
	"os"

	"github.com/aianyai/sidera/cmd/sidera/internal/cli"
	"github.com/aianyai/sidera/cmd/sidera/internal/launch"
	"github.com/aianyai/sidera/internal/release"
)

func main() {
	// First, before anything changes the working directory.
	started, startedErr := launch.Capture()
	os.Exit(cli.Run(context.Background(), os.Args, cli.Config{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		Executable: os.Executable,
		Release:    release.Read(),
		Launch:     started, LaunchErr: startedErr,
	}))
}
