package adapterhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// A state directory that is there is judged, not trusted: it is protected on its
// own, so the owner-only data directory says nothing of it. One that someone else
// can reach, or a file in it that holds a token, is refused naming the path; what
// is in another instance's directory is not this instance's to judge.
func TestMakeStateDirRefusesAnExistingDirectoryOthersCanReach(t *testing.T) {
	for _, tc := range []struct {
		name    string
		widen   string
		refused bool
	}{
		{"a clean tree", "", false},
		{"adapter-state", "adapter-state", true},
		{"the state directory of the instance", "adapter-state/chat", true},
		{"a directory in it", "adapter-state/chat/turns", true},
		{"the token file in it", "adapter-state/chat/ws-token", true},
		{"a file of another instance", "adapter-state/other/ws-token", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "h")
			if err := fsx.MakeRoot(home); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{"adapter-state", "adapter-state/chat", "adapter-state/other"} {
				if err := fsx.MakeRoot(filepath.Join(home, dir)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(home, "adapter-state", "chat", "turns"), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{"adapter-state/chat/ws-token", "adapter-state/other/ws-token"} {
				if err := os.WriteFile(filepath.Join(home, filepath.FromSlash(file)), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			p := filepath.Join(home, filepath.FromSlash(tc.widen))
			if tc.widen != "" {
				widen(t, p)
			}
			err := makeStateDir(home, "chat")
			switch {
			case !tc.refused && err != nil:
				t.Fatalf("makeStateDir = %v, want nil", err)
			case tc.refused && (err == nil || !strings.Contains(err.Error(), p)):
				t.Fatalf("makeStateDir = %v, want a refusal naming %s", err, p)
			}
		})
	}
}
