package record

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct{ name, path, want string }{
		{"separators and punctuation become hyphens", "/Users/u/work", "-Users-u-work"},
		{"letters and digits are kept", "/a1/B2", "-a1-B2"},
		{"dots, underscores, spaces and hyphens all become hyphens", "/a.b_c d-e", "-a-b-c-d-e"},
		{"a precomposed letter is one hyphen", "/caf\u00e9", "-caf-"},
		{"a decomposed letter is composed first, so also one hyphen", "/cafe\u0301", "-caf-"},
		{"a character outside the basic plane is two hyphens", "/a\U0001F600b", "-a--b"},
		{"a Windows path", `C:\Users\u`, "C--Users-u"},
		{"a real run of the runtime", "/private/tmp/claude-501/-Users-x/s1a/work", "-private-tmp-claude-501--Users-x-s1a-work"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.path); got != tc.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestDir(t *testing.T) {
	projects := filepath.Join("base", "projects")
	got, err := Dir(projects, "/Users/u/work")
	if err != nil || got != filepath.Join(projects, "-Users-u-work") {
		t.Fatalf("Dir = %q, %v", got, err)
	}
	if got := File(got, "abc"); filepath.Base(got) != "abc.jsonl" {
		t.Fatalf("File = %q", got)
	}

	if _, err := Dir(projects, "/"+strings.Repeat("a", MaxDirName)); !errors.Is(err, ErrDirNameTooLong) {
		t.Fatalf("a name of %d characters: %v", MaxDirName+1, err)
	}
	if _, err := Dir(projects, "/"+strings.Repeat("a", MaxDirName-1)); err != nil {
		t.Fatalf("a name of %d characters: %v", MaxDirName, err)
	}
	for _, args := range [][2]string{{"", "/x"}, {projects, ""}} {
		if _, err := Dir(args[0], args[1]); err == nil {
			t.Fatalf("Dir(%q, %q): want an error", args[0], args[1])
		}
	}
}

const (
	sess   = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
	prompt = "0d72db02-5ca8-408c-be30-8e71c034c750"
	other  = "11111111-2222-4333-8444-555555555555"
)

func TestIndex(t *testing.T) {
	queue := `{"type":"queue-operation","operation":"enqueue","sessionId":"` + sess + `"}`
	user := `{"type":"user","uuid":"` + prompt + `","promptSource":"sdk","sessionId":"` + sess + `"}`
	attach := `{"type":"attachment","uuid":"` + prompt + `","sessionId":"` + sess + `"}`
	toolResult := `{"type":"user","uuid":"` + other + `","sessionId":"` + sess + `"}`
	unrelated := `{"type":"user","uuid":"` + prompt + `","sessionId":"` + other + `"}`
	mention := `{"type":"assistant","message":"` + prompt + `","sessionId":"` + sess + `"}`

	tests := []struct {
		name         string
		file         string
		wantSession  bool
		wantPrompt   bool
		wantOtherUse bool
	}{
		{"an empty file", "", false, false, false},
		{"queue operations name the session but are no prompt", queue + "\n" + queue + "\n", true, false, false},
		{"the user record with the uuid is the prompt", queue + "\n" + user + "\n" + attach + "\n", true, true, false},
		{"a line that is not a user record does not count", attach + "\n" + mention + "\n", true, false, false},
		{"another user record is not the prompt", queue + "\n" + toolResult + "\n", true, false, false},
		{"the prompt is found after many other lines", strings.Repeat(attach+"\n", 1000) + user + "\n", true, true, false},
		{"a last line without its newline is not judged yet", queue + "\n" + user, true, false, false},
		{"a corrupt line is skipped", "{" + prompt + "\n" + user + "\n", true, true, false},
		{"a user record with the uuid counts whatever session id it names, since uuids are unique", unrelated + "\n", true, true, false},
	}
	for _, tc := range tests {
		for _, chunk := range []int{len(tc.file) + 1, 7, 1} {
			name := tc.name
			if chunk <= 7 {
				name += " (fed in pieces)"
			}
			t.Run(name, func(t *testing.T) {
				ix := NewIndex(sess, prompt)
				for off := 0; off < len(tc.file); off += chunk {
					end := min(off+chunk, len(tc.file))
					ix.Feed([]byte(tc.file[off:end]))
				}
				if ix.SessionSeen() != tc.wantSession || ix.UserSeen(prompt) != tc.wantPrompt || ix.UserSeen(other) != tc.wantOtherUse {
					t.Fatalf("session=%v prompt=%v other=%v, want %v %v %v", ix.SessionSeen(), ix.UserSeen(prompt), ix.UserSeen(other),
						tc.wantSession, tc.wantPrompt, tc.wantOtherUse)
				}
				if ix.Offset() != int64(len(tc.file)) {
					t.Fatalf("offset %d, want %d", ix.Offset(), len(tc.file))
				}
			})
		}
	}
}

func TestIndexWatchesUUIDsAddedLater(t *testing.T) {
	ix := NewIndex(sess)
	ix.Feed([]byte(`{"type":"user","uuid":"` + prompt + `","sessionId":"` + sess + `"}` + "\n"))
	if ix.UserSeen(prompt) {
		t.Fatal("a uuid not watched when the line was fed is not remembered")
	}
	ix.Watch(prompt)
	ix.Feed([]byte(`{"type":"user","uuid":"` + prompt + `","sessionId":"` + sess + `"}` + "\n"))
	if !ix.UserSeen(prompt) {
		t.Fatal("watched uuid not found")
	}
}

func TestLastResult(t *testing.T) {
	ok := `{"type":"result","subtype":"success","is_error":false,"result":"OK"}`
	failed := `{"type":"result","subtype":"error_during_execution","is_error":true}`
	soft := `{"type":"result","subtype":"success","is_error":true}`
	maxTurns := `{"type":"result","subtype":"error_max_turns","is_error":false}`
	noise := `{"type":"assistant","message":{"content":[{"type":"text","text":"{\"type\":\"result\"}"}]}}`
	tests := []struct {
		name string
		out  string
		want Outcome
	}{
		{"an empty stream was cut off", "", NoResult},
		{"no result line", noise + "\n" + `{"type":"system"}` + "\n", NoResult},
		{"one success", noise + "\n" + ok + "\n", Succeeded},
		{"one error", failed + "\n", Errored},
		{"a success marked as an error is an error", soft + "\n", Errored},
		{"a non-success subtype is an error", maxTurns + "\n", Errored},
		{"the last of several results decides: success after error", failed + "\n" + ok + "\n", Succeeded},
		{"the last of several results decides: error after success", ok + "\n" + failed + "\n", Errored},
		{"text that merely quotes a result line is not one", noise + "\n", NoResult},
		{"a last line without a newline still counts", ok, Succeeded},
		{"a last line cut in half is ignored", ok + "\n" + `{"type":"result","subtype":"succ`, Succeeded},
		{"a line longer than any buffer", ok + "\n" + `{"type":"assistant","x":"` + strings.Repeat("a", 1<<20) + `"}` + "\n", Succeeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LastResult(strings.NewReader(tc.out))
			if err != nil || got != tc.want {
				t.Fatalf("LastResult = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
