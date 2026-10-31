package declare

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/sdk/go/manifest"
)

// The manifest and the capabilities the adapter answers initialize with are valid
// for every platform it is packaged for.
func TestManifestAndCapabilitiesAreValid(t *testing.T) {
	if err := Capabilities().Validate(); err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	for _, p := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}} {
		raw, err := manifest.Encode(Manifest(p[0], p[1]))
		if err != nil {
			t.Fatalf("%v: %v", p, err)
		}
		m, err := manifest.Parse(raw)
		if err != nil {
			t.Fatalf("%v: the encoded manifest does not parse: %v", p, err)
		}
		if len(m.Platforms) != 1 || m.Platforms[0].OS != p[0] || m.Platforms[0].Arch != p[1] {
			t.Errorf("%v: platforms = %v", p, m.Platforms)
		}
	}
	if got := Manifest("windows", "amd64").Entry.Program; got != "bin/sidera-adapter-codex.exe" {
		t.Errorf("windows entry = %s", got)
	}
}

func TestVersionTested(t *testing.T) {
	for v, want := range map[string]bool{"0.164.0": true, "0.163.9": false, "0.165.0": false, "garbage": false} {
		if got := VersionTested(v); got != want {
			t.Errorf("VersionTested(%s) = %v, want %v", v, got, want)
		}
	}
}

func TestWritePackage(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "built")
	if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "pkg")
	if err := WritePackage(out, bin, "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "bin", "sidera-adapter-codex"))
	if err != nil || string(got) != "binary" {
		t.Fatalf("binary = %q, %v", got, err)
	}
	raw, err := os.ReadFile(filepath.Join(out, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Parse(raw); err != nil {
		t.Fatal(err)
	}
}
