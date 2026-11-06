package main

import (
	"fmt"
	"slices"

	"github.com/aianyai/sidera/sdk/go/manifest"
)

// writePackage writes the manifest built into the executable and a copy of the
// binary at its entry into dir. A package holds one binary, so its platforms is the
// one it is written for, which the manifest must list: the platforms the manifest
// lists are the ones the adapter runs on, and the installation checks the list.
func writePackage(goos, goarch, binary, dir string) error {
	m, err := loadManifest()
	if err != nil {
		return err
	}
	platform := manifest.Platform{OS: goos, Arch: goarch}
	if !slices.Contains(m.Platforms, platform) {
		return fmt.Errorf("the manifest does not list the platform %s/%s", goos, goarch)
	}
	m.Platforms = []manifest.Platform{platform}
	return manifest.WritePackage(dir, m, binary)
}
