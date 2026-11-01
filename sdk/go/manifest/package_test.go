package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A package written from a manifest is one the daemon reads: the manifest parses to
// what was given and the binary is at the program, executable. A manifest that is
// invalid or a binary that is missing writes nothing.
func TestWritePackage(t *testing.T) {
	m := Manifest{
		ManifestVersion: Version,
		ID:              "x",
		Version:         "1.0.0",
		Protocol:        adapterproto.Range{Min: 1, Max: 1},
		Entry:           Entry{Program: "bin/sidera-adapter-x"},
		Platforms:       []Platform{{OS: "linux", Arch: "amd64"}},
	}
	bin := filepath.Join(t.TempDir(), "built")
	if err := os.WriteFile(bin, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "pkg")
	if err := WritePackage(dir, m, bin); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(raw)
	if err != nil || got.ID != "x" || got.Entry.Program != "bin/sidera-adapter-x" {
		t.Fatalf("manifest %+v, %v", got, err)
	}
	file := filepath.Join(dir, "bin", "sidera-adapter-x")
	if data, err := os.ReadFile(file); err != nil || string(data) != "binary" {
		t.Fatalf("binary %q, %v", data, err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the binary is not executable: %v, %v", info, err)
	}

	for name, tc := range map[string]struct {
		m      Manifest
		binary string
	}{
		"an invalid manifest": {Manifest{}, bin},
		"a missing binary":    {m, filepath.Join(t.TempDir(), "none")},
	} {
		empty := filepath.Join(t.TempDir(), "pkg")
		if err := WritePackage(empty, tc.m, tc.binary); err == nil {
			t.Errorf("%s: no error", name)
		}
		if _, err := os.Stat(empty); !os.IsNotExist(err) {
			t.Errorf("%s: something was written (%v)", name, err)
		}
	}
}

// A package built for Windows names its program with the extension the file has,
// whether or not the manifest gave it: the installer looks for the file the
// manifest names.
func TestWritePackageForWindowsNamesTheProgramWithItsExtension(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "built")
	if err := os.WriteFile(bin, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, program := range []string{"bin/sidera-adapter-x", "bin/sidera-adapter-x.exe"} {
		m := Manifest{
			ManifestVersion: Version,
			ID:              "x",
			Version:         "1.0.0",
			Protocol:        adapterproto.Range{Min: 1, Max: 1},
			Entry:           Entry{Program: program},
			Platforms:       []Platform{{OS: "windows", Arch: "amd64"}},
		}
		dir := filepath.Join(t.TempDir(), "pkg")
		if err := WritePackage(dir, m, bin); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, FileName))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Parse(raw)
		if err != nil || got.Entry.Program != "bin/sidera-adapter-x.exe" {
			t.Fatalf("%s: manifest %+v, %v", program, got, err)
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(got.Entry.Program))); err != nil {
			t.Errorf("%s: the program the manifest names is not in the package: %v", program, err)
		}
	}
}
