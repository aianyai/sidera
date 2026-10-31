package console

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/sdk/go/manifest"
)

func TestEveryTargetGetsAManifestTheDaemonAccepts(t *testing.T) {
	for _, p := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}} {
		t.Run(p[0]+"/"+p[1], func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "built")
			if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "pkg")
			if err := writePackage(p[0], p[1], bin, out); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(out, manifest.FileName))
			if err != nil {
				t.Fatal(err)
			}
			m, err := manifest.Parse(data)
			if err != nil {
				t.Fatalf("the manifest is refused: %v", err)
			}
			if m.ID != AdapterID || m.Version != Version || len(m.Platforms) != 1 || m.Platforms[0].OS != p[0] || m.Platforms[0].Arch != p[1] {
				t.Errorf("manifest %+v", m)
			}
			file := "sidera-adapter-console"
			if p[0] == "windows" {
				file += ".exe"
			}
			if _, err := os.Stat(filepath.Join(out, "bin", file)); err != nil {
				t.Errorf("the binary is not in the package: %v", err)
			}
			if err := writePackage(p[0], p[1], bin, out); err == nil {
				t.Error("a package was written over a directory that is not empty")
			}
		})
	}
	if _, err := Manifest("haiku", "arm64"); err == nil {
		t.Error("a manifest was made for an unknown platform")
	}
}
