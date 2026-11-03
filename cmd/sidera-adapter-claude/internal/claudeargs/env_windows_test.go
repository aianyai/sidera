//go:build windows

package claudeargs

import (
	"reflect"
	"testing"
)

// Windows compares variable names without case, and a base environment may spell
// them any way: a session must not keep a stripped variable or lose the sidera
// directory because of the spelling.
func TestEnvironNamesAreComparedWithoutCaseOnWindows(t *testing.T) {
	base := []string{`Path=C:\Windows`, "claudecode=1", "Claude_Code_Entrypoint=cli", `claude_code_git_bash_path=C:\Git\bash.exe`, "Anthropic_Api_Key=k"}
	got := Environ(base, map[string]string{"SIDERA_HOME": `C:\s`}, `C:\sidera`)
	want := []string{`Path=C:\sidera;C:\Windows`, `claude_code_git_bash_path=C:\Git\bash.exe`, `SIDERA_HOME=C:\s`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if dir := ConfigDir([]string{`claude_config_dir=C:\cfg`}); dir != `C:\cfg` {
		t.Fatalf("ConfigDir = %q", dir)
	}
}
