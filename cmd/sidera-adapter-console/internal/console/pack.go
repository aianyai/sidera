package console

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
		Version:         Version,
		Protocol:        adapterproto.SupportedRange(),
		// The Windows file is bin/sidera-adapter-console.exe; the host finds it.
		Entry:     manifest.Entry{Program: "bin/" + ProgramName, Args: []string{"serve"}},
		Platforms: []manifest.Platform{{OS: goos, Arch: goarch}},
	}
	return m, m.Validate()
}

// runPackage is `package`: it writes the manifest and copies the binary built for
// the platform into a new package directory, ready for `sidera adapter install`.
func runPackage(args []string, env Env) int {
	fs := flag.NewFlagSet("package", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var goos, goarch, binary, out string
	fs.StringVar(&goos, "goos", "", "")
	fs.StringVar(&goarch, "goarch", "", "")
	fs.StringVar(&binary, "binary", "", "")
	fs.StringVar(&out, "out", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || goos == "" || goarch == "" || binary == "" || out == "" {
		fmt.Fprintln(env.Stderr, "sidera-adapter-console: package needs --goos, --goarch, --binary and --out")
		return exitUsage
	}
	if err := writePackage(goos, goarch, binary, out); err != nil {
		fmt.Fprintln(env.Stderr, "sidera-adapter-console:", err)
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
	program := ProgramName
	if goos == "windows" {
		program += ".exe"
	}
	bin, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(out, "bin"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "bin", program), bin, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, manifest.FileName), append(data, '\n'), 0o644)
}
