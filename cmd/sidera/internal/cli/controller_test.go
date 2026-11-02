package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// An agent command run inside a session a person opened attaches it after its
// arguments are checked: a command refused for them reaches no node, which here does
// not exist and would otherwise be the command's error.
func TestACommandRefusedForItsArgumentsAttachesNothing(t *testing.T) {
	env := map[string]string{"CLAUDE_CODE_SESSION_ID": "session-1"}
	// A short path that does not exist: a long one is refused before any command runs.
	absent := filepath.VolumeName(os.TempDir()) + string(filepath.Separator) + "no-sidera-home"
	code, _, errs := runCLI(t, env, "--home", absent, "recv", "--no-such-flag")
	if code != 2 {
		t.Fatalf("exit %d, want 2 (usage); stderr %q", code, errs)
	}
}
