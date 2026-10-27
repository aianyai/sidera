package config_test

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

func osPackage(t *testing.T) (fsx.Reader, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		manifest.FileName:            manifestOf("fresh", "0.1.0"),
		"bin/sidera-adapter-example": "#!program\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src, err := fsx.OpenOS(root, suffixReader())
	if err != nil {
		t.Fatal(err)
	}
	return src, root
}

func TestStagingFollowsOnlyReadableRegularFileLinksWithinTheSizeLimit(t *testing.T) {
	for _, kind := range []string{"regular", "directory", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			rg, c := loaded(t)
			before := snapshot(t, rg.mem)
			src, root := osPackage(t)
			target := filepath.Join(t.TempDir(), "target")
			if kind == "directory" {
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(target, []byte("linked bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
				if kind == "oversized" {
					// Extending a file does not read its bytes into the test process.
					if err := os.Truncate(target, config.MaxPackageBytes+1); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := os.Symlink(target, filepath.Join(root, "payload")); err != nil {
				t.Skipf("cannot create a symlink here: %v", err)
			}
			staged, err := c.StageAdapter(src, suffixReader())
			switch kind {
			case "regular":
				if err != nil {
					t.Fatal(err)
				}
				data, err := rg.mem.ReadFile(path.Join(staged.Dir, "payload"))
				if err != nil || string(data) != "linked bytes" {
					t.Fatalf("staged link content = %q, %v", data, err)
				}
				if err := c.Unstage(staged); err != nil {
					t.Fatal(err)
				}
			case "directory":
				var source *config.SourceError
				if !errors.As(err, &source) || source.Path != "payload" || !errors.Is(err, fsx.ErrNotRegular) {
					t.Fatalf("StageAdapter = %v, want SourceError for payload", err)
				}
			case "oversized":
				var change *config.ChangeError
				if !errors.As(err, &change) || change.Reason != config.ChangeInvalid || change.Field != "payload" || !strings.Contains(err.Error(), "256 MiB") {
					t.Fatalf("StageAdapter = %v, want invalid payload naming the size limit", err)
				}
			}
			if after := snapshot(t, rg.mem); !reflect.DeepEqual(before, after) {
				t.Errorf("stage left %v", diffKeys(before, after))
			}
		})
	}
}
