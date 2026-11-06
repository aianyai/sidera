package console

import (
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// Manifest is the manifest of the package for one platform. It is written from the
// constants that initialize answers from, so the two cannot differ. A package
// holds one binary, so its platforms is that one: the install check is whether the
// package runs here.
func Manifest(goos, goarch string) (manifest.Manifest, error) {
	m := manifest.Manifest{
		ManifestVersion: manifest.Version,
		ID:              AdapterID,
		Version:         release.Version(),
		Protocol:        adapterproto.SupportedRange(),
		// manifest.WritePackage adds .exe to the program of a Windows package.
		Entry:     manifest.Entry{Program: "bin/" + ProgramName, Args: []string{"serve"}},
		Platforms: []manifest.Platform{{OS: goos, Arch: goarch}},
	}
	return m, m.Validate()
}

// writePackage writes the manifest and copies the binary built for the platform
// into the package directory, ready for `sidera adapter install`.
func writePackage(goos, goarch, binary, out string) error {
	m, err := Manifest(goos, goarch)
	if err != nil {
		return err
	}
	return manifest.WritePackage(out, m, binary)
}
