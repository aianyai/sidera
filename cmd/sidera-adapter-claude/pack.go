package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"

	"github.com/aianyai/sidera/sdk/go/manifest"
)

// packageCommand is `package`: it writes the adapter package for one platform from
// a binary built for it, ready for `sidera adapter install`.
func packageCommand(args []string) error {
	fs := flag.NewFlagSet("package", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	goos := fs.String("goos", "", "operating system the binary was built for (GOOS)")
	goarch := fs.String("goarch", "", "architecture the binary was built for (GOARCH)")
	binary := fs.String("binary", "", "the binary built for that platform")
	out := fs.String("out", "", "the directory to write the package into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *goos == "" || *goarch == "" || *binary == "" || *out == "" || fs.NArg() != 0 {
		return errors.New("package needs --goos, --goarch, --binary and --out")
	}
	return writePackage(*out, *binary, *goos, *goarch)
}

// writePackage writes the manifest built into the executable and a copy of the
// binary at its entry into dir. A package holds one binary, so its platforms is the
// one it is written for, which the manifest must list: the platforms the manifest
// lists are the ones the adapter runs on, and the installation checks the list.
func writePackage(dir, binary, goos, goarch string) error {
	m, err := manifest.Parse(manifestJSON)
	if err != nil {
		return fmt.Errorf("the manifest built into the executable: %w", err)
	}
	platform := manifest.Platform{OS: goos, Arch: goarch}
	if !slices.Contains(m.Platforms, platform) {
		return fmt.Errorf("the manifest does not list the platform %s/%s", goos, goarch)
	}
	m.Platforms = []manifest.Platform{platform}
	return manifest.WritePackage(dir, m, binary)
}
