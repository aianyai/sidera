package adaptersetup_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// allow is the edit the Claude setup makes: add a rule to permissions.allow.
func allow(rule string) func(*adaptersetup.Object) (bool, error) {
	return func(root *adaptersetup.Object) (bool, error) {
		return root.AddString([]string{"permissions", "allow"}, rule)
	}
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noTemporaries(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func symlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}
}

// The edit keeps what the person wrote: the order of the members, the spelling of
// numbers, the indentation, the line ending and the final newline.
func TestEditJSONFileKeepsTheStyleOfTheFile(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
	}{
		{"two spaces", "{\n  \"b\": 1.50,\n  \"a\": [\n    true,\n    null\n  ],\n  \"permissions\": {\n    \"deny\": []\n  }\n}\n",
			"{\n  \"b\": 1.50,\n  \"a\": [\n    true,\n    null\n  ],\n  \"permissions\": {\n    \"deny\": [],\n    \"allow\": [\n      \"mcp__sidera\"\n    ]\n  }\n}\n"},
		{"tabs, no final newline", "{\n\t\"x\": 1e3\n}", "{\n\t\"x\": 1e3,\n\t\"permissions\": {\n\t\t\"allow\": [\n\t\t\t\"mcp__sidera\"\n\t\t]\n\t}\n}"},
		{"four spaces and CRLF", "{\r\n    \"x\": \"<&>\"\r\n}\r\n", "{\r\n    \"x\": \"<&>\",\r\n    \"permissions\": {\r\n        \"allow\": [\r\n            \"mcp__sidera\"\r\n        ]\r\n    }\r\n}\r\n"},
		{"compact", `{"x":[1,2]}`, `{"x":[1,2],"permissions":{"allow":["mcp__sidera"]}}`},
		{"compact with a final newline", "{\"x\":[1,2]}\n", "{\"x\":[1,2],\"permissions\":{\"allow\":[\"mcp__sidera\"]}}\n"},
		{"the unit is that of the first member of the object, not of the first indented line",
			"{\"a\": {\n      \"b\": 1\n    },\n  \"c\": 2\n}\n",
			"{\n  \"a\": {\n    \"b\": 1\n  },\n  \"c\": 2,\n  \"permissions\": {\n    \"allow\": [\n      \"mcp__sidera\"\n    ]\n  }\n}\n"},
		{"empty object", "{}\n", "{\n  \"permissions\": {\n    \"allow\": [\n      \"mcp__sidera\"\n    ]\n  }\n}\n"},
		{"escapes survive", `{"s":"aé\n\"\\/"}`, `{"s":"aé\n\"\\/","permissions":{"allow":["mcp__sidera"]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			write(t, path, tc.before, 0o600)
			written, err := adaptersetup.EditJSONFile(path, allow("mcp__sidera"))
			if err != nil || !written {
				t.Fatalf("EditJSONFile = %v, %v", written, err)
			}
			if got := read(t, path); got != tc.after {
				t.Errorf("file =\n%q\nwant\n%q", got, tc.after)
			}
			if !json.Valid([]byte(read(t, path))) {
				t.Error("the result is not valid JSON")
			}
			noTemporaries(t, filepath.Dir(path))
		})
	}
}

func TestEditJSONFileLeavesAFileAloneWhenThereIsNothingToChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	const content = "{ \"permissions\" : {\"allow\":[ \"mcp__sidera\" ]} }\n" // odd spacing survives
	write(t, path, content, 0o600)
	before, _ := os.Stat(path)
	written, err := adaptersetup.EditJSONFile(path, allow("mcp__sidera"))
	after, _ := os.Stat(path)
	if err != nil || written || read(t, path) != content || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("written %v, err %v, content %q", written, err, read(t, path))
	}
}

func TestEditJSONFileMakesAFileThatIsNotThere(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "claude", "config") // neither directory exists
	path := filepath.Join(dir, "settings.json")
	written, err := adaptersetup.EditJSONFile(path, allow("mcp__sidera"))
	if err != nil || !written {
		t.Fatalf("EditJSONFile = %v, %v", written, err)
	}
	if got, want := read(t, path), "{\n  \"permissions\": {\n    \"allow\": [\n      \"mcp__sidera\"\n    ]\n  }\n}\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
		if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
			t.Errorf("directory mode = %v, want 0700", info.Mode().Perm())
		}
	}
}

func TestEditJSONFileKeepsTheMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a mode has no effect on Windows")
	}
	for _, mode := range []os.FileMode{0o600, 0o640, 0o644, 0o400 | 0o200} {
		path := filepath.Join(t.TempDir(), "settings.json")
		write(t, path, "{}", mode)
		if _, err := adaptersetup.EditJSONFile(path, allow("r")); err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != mode {
			t.Errorf("mode after the edit = %v, want %v", info.Mode().Perm(), mode)
		}
	}
}

// A settings file that is a link into a dotfiles directory stays a link, and the file
// it leads to is the one written.
func TestEditJSONFileFollowsLinks(t *testing.T) {
	t.Run("absolute", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "dotfiles", "settings.json")
		write(t, target, "{\"a\": 1}\n", 0o640)
		link := filepath.Join(dir, "home", "settings.json")
		os.MkdirAll(filepath.Dir(link), 0o755)
		symlink(t, target, link)
		if _, err := adaptersetup.EditJSONFile(link, allow("r")); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link was replaced: %v, %v", info, err)
		}
		if !strings.Contains(read(t, target), `"r"`) {
			t.Errorf("target = %q", read(t, target))
		}
		if info, _ := os.Stat(target); runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
			t.Errorf("target mode = %v", info.Mode().Perm())
		}
		noTemporaries(t, filepath.Dir(link))
		noTemporaries(t, filepath.Dir(target))
	})
	t.Run("relative, to another directory", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "dotfiles", "settings.json")
		write(t, target, "{}", 0o600)
		link := filepath.Join(dir, "home", "settings.json")
		os.MkdirAll(filepath.Dir(link), 0o755)
		symlink(t, filepath.Join("..", "dotfiles", "settings.json"), link)
		if _, err := adaptersetup.EditJSONFile(link, allow("r")); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(read(t, target), `"r"`) {
			t.Errorf("target = %q", read(t, target))
		}
	})
	// The kernel resolves a relative link from the directory the link is really in, not
	// from the path it was reached by: here .claude is itself a link into dotfiles.
	t.Run("relative, inside a directory that is a link", func(t *testing.T) {
		for _, decoy := range []bool{false, true} {
			t.Run(map[bool]string{false: "no other file", true: "a file where the lexical path leads"}[decoy], func(t *testing.T) {
				dir := t.TempDir()
				real := filepath.Join(dir, "dotfiles", "shared", "settings.json")
				write(t, real, "{\"a\": 1}\n", 0o600)
				write(t, filepath.Join(dir, "dotfiles", "claude", ".keep"), "", 0o600)
				symlink(t, filepath.Join("..", "shared", "settings.json"), filepath.Join(dir, "dotfiles", "claude", "settings.json"))
				os.MkdirAll(filepath.Join(dir, "home"), 0o755)
				symlink(t, filepath.Join("..", "dotfiles", "claude"), filepath.Join(dir, "home", ".claude"))
				// Joined to the path it was reached by, the link leads to home/shared/settings.json.
				lexical := filepath.Join(dir, "home", "shared", "settings.json")
				if decoy {
					write(t, lexical, "{\"decoy\": true}\n", 0o600)
				}
				if _, err := adaptersetup.EditJSONFile(filepath.Join(dir, "home", ".claude", "settings.json"), allow("r")); err != nil {
					t.Fatalf("decoy %v: %v", decoy, err)
				}
				if got := read(t, real); !strings.Contains(got, `"r"`) || !strings.Contains(got, `"a"`) {
					t.Errorf("decoy %v: the file the link leads to = %q", decoy, got)
				}
				if _, err := os.Stat(lexical); decoy && read(t, lexical) != "{\"decoy\": true}\n" || !decoy && err == nil {
					t.Errorf("decoy %v: the file beside the path it was reached by was made or changed", decoy)
				}
			})
		}
	})
	t.Run("a chain", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.json")
		write(t, target, "{}", 0o600)
		symlink(t, target, filepath.Join(dir, "b.json"))
		symlink(t, "b.json", filepath.Join(dir, "a.json"))
		if _, err := adaptersetup.EditJSONFile(filepath.Join(dir, "a.json"), allow("r")); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(read(t, target), `"r"`) {
			t.Errorf("target = %q", read(t, target))
		}
	})
	t.Run("dangling, the target is made", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "settings.json")
		symlink(t, "dotfiles-settings.json", link) // relative, and not there
		if _, err := adaptersetup.EditJSONFile(link, allow("r")); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link was replaced: %v, %v", info, err)
		}
		if !strings.Contains(read(t, filepath.Join(dir, "dotfiles-settings.json")), `"r"`) {
			t.Error("the target was not made")
		}
	})
	t.Run("dangling into a directory that is not there", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "settings.json")
		symlink(t, filepath.Join(dir, "gone", "settings.json"), link)
		_, err := adaptersetup.EditJSONFile(link, allow("r"))
		if err == nil || !strings.Contains(err.Error(), "directory does not exist") {
			t.Errorf("err = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "gone")); statErr == nil {
			t.Error("the directory was made behind a link")
		}
	})
	t.Run("a loop", func(t *testing.T) {
		dir := t.TempDir()
		symlink(t, "b.json", filepath.Join(dir, "a.json"))
		symlink(t, "a.json", filepath.Join(dir, "b.json"))
		if _, err := adaptersetup.EditJSONFile(filepath.Join(dir, "a.json"), allow("r")); err == nil {
			t.Error("a loop of links was edited")
		}
	})
}

// A file that is not strict JSON is refused as it is, with the reason, and the edit
// is never offered it.
func TestEditJSONFileRefusesWhatIsNotStrictJSON(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"a comment", "{\n  // allow\n  \"a\": 1\n}\n"},
		{"a block comment", `{"a": /* x */ 1}`},
		{"a trailing comma", `{"a": 1,}`},
		{"a byte-order mark", "\xef\xbb\xbf{}"},
		{"empty", ""},
		{"blank", "  \n"},
		{"an array", `[]`},
		{"a scalar", `"x"`},
		{"a repeated key", `{"a":1,"a":2}`},
		{"a repeated key below", `{"a":{"b":1,"b":2}}`},
		{"data after", `{} {}`},
		{"truncated", `{"a": `},
		{"invalid UTF-8 in a string", "{\"a\": \"\xff\"}"},
		{"invalid UTF-8 in a key", "{\"\xc3\": 1}"},
		{"an unpaired high surrogate", `{"a": "\ud800"}`},
		{"an unpaired low surrogate", `{"a": "x\udc00"}`},
		{"a high surrogate and no low one", `{"a": "\ud83d\u0041"}`},
		{"an unpaired surrogate in a key", `{"\ud800": 1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			write(t, path, tc.content, 0o600)
			offered := false
			written, err := adaptersetup.EditJSONFile(path, func(*adaptersetup.Object) (bool, error) { offered = true; return true, nil })
			if !errors.Is(err, adaptersetup.ErrInvalidJSON) || written || offered {
				t.Errorf("written %v, offered %v, err %v; want ErrInvalidJSON", written, offered, err)
			}
			if err != nil && !strings.Contains(err.Error(), "nothing was changed") {
				t.Errorf("the error does not say that the file was not touched: %v", err)
			}
			if read(t, path) != tc.content {
				t.Error("the file was touched")
			}
			noTemporaries(t, filepath.Dir(path))
		})
	}
}

// The file is read again just before the rename. Another writer (Claude Code
// writes this file too) wins, and the edit is dropped.
func TestEditJSONFileDropsAnEditWhenTheFileChangedUnderIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before string // empty: the file does not exist
		other  func(path string) error
		want   string // what the file holds afterwards; "" for gone
	}{
		{"rewritten", "{\"a\":1}\n", func(p string) error { return os.WriteFile(p, []byte("{\"a\":2}\n"), 0o600) }, "{\"a\":2}\n"},
		{"appended", "{}", func(p string) error {
			f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = f.WriteString("\n")
			return err
		}, "{}\n"},
		{"removed", "{}", os.Remove, ""},
		{"made by someone else", "", func(p string) error { return os.WriteFile(p, []byte("{\"new\":1}"), 0o600) }, "{\"new\":1}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if tc.before != "" {
				write(t, path, tc.before, 0o600)
			}
			written, err := adaptersetup.EditJSONFile(path, func(root *adaptersetup.Object) (bool, error) {
				root.Set("mine", true)
				return true, tc.other(path)
			})
			if !errors.Is(err, adaptersetup.ErrChangedDuringEdit) || written {
				t.Fatalf("written %v, err %v; want ErrChangedDuringEdit", written, err)
			}
			got, readErr := os.ReadFile(path)
			if tc.want == "" && !errors.Is(readErr, os.ErrNotExist) || tc.want != "" && string(got) != tc.want {
				t.Errorf("file = %q, %v; want %q", got, readErr, tc.want)
			}
			noTemporaries(t, filepath.Dir(path))
		})
	}
}

func TestEditJSONFileErrorsLeaveTheFileAlone(t *testing.T) {
	t.Run("the edit fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		write(t, path, "{}", 0o600)
		boom := errors.New("boom")
		written, err := adaptersetup.EditJSONFile(path, func(root *adaptersetup.Object) (bool, error) { root.Set("x", true); return true, boom })
		if !errors.Is(err, boom) || written || read(t, path) != "{}" {
			t.Errorf("written %v, err %v, file %q", written, err, read(t, path))
		}
	})
	t.Run("a directory is not a settings file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		os.Mkdir(path, 0o755)
		if _, err := adaptersetup.EditJSONFile(path, allow("r")); err == nil {
			t.Error("a directory was edited")
		}
	})
	t.Run("the directory cannot be written", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs a directory this user cannot write")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		write(t, path, "{}", 0o600)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0o700)
		written, err := adaptersetup.EditJSONFile(path, allow("r"))
		if err == nil || written || read(t, path) != "{}" {
			t.Errorf("written %v, err %v, file %q", written, err, read(t, path))
		}
		noTemporaries(t, dir)
	})
}

// The typed navigation refuses a value of the wrong type instead of panicking on it or
// overwriting what the person wrote, and leaves the file as it was.
func TestEditJSONFileRefusesTheWrongTypeAndKeepsTheFile(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"permissions is an array", `{"permissions": []}`, "permissions is an array, not an object"},
		{"permissions is a number", `{"permissions": 3}`, "permissions is a number, not an object"},
		{"permissions is null", `{"permissions": null}`, "permissions is null, not an object"},
		{"allow is a string", `{"permissions": {"allow": "mcp__sidera"}}`, "permissions.allow is a string, not an array"},
		{"allow is an object", `{"permissions": {"allow": {}}}`, "permissions.allow is an object, not an array"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			write(t, path, tc.content, 0o600)
			written, err := adaptersetup.EditJSONFile(path, allow("r"))
			if !errors.Is(err, adaptersetup.ErrUnexpectedType) || written || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("written %v, err %v; want ErrUnexpectedType saying %q", written, err, tc.want)
			}
			if read(t, path) != tc.content {
				t.Errorf("the file changed: %q", read(t, path))
			}
			root, _, _ := adaptersetup.ReadJSONFile(path)
			for _, probe := range []func() error{
				func() error { _, err := root.HasString([]string{"permissions", "allow"}, "r"); return err },
				func() error { _, err := root.RemoveString([]string{"permissions", "allow"}, "r"); return err },
			} {
				if err := probe(); !errors.Is(err, adaptersetup.ErrUnexpectedType) {
					t.Errorf("the same file read through the other helpers: %v", err)
				}
			}
		})
	}
}

func TestObjectStringArrays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	write(t, path, "{\n  \"permissions\": {\n    \"allow\": [\"a\", 7, \"b\", \"a\", null]\n  }\n}\n", 0o600)
	rules := []string{"permissions", "allow"}
	wrongNumber, _, _ := adaptersetup.ReadJSONFile(writeTemp(t, `{"x": 1}`))

	// Duplicates and other types: a present string is not added again, every copy is
	// removed, and what is not a string stays.
	root, exists, err := adaptersetup.ReadJSONFile(path)
	if err != nil || !exists {
		t.Fatal(exists, err)
	}
	if has, _ := root.HasString(rules, "b"); !has {
		t.Error("HasString(b) = false")
	}
	if has, _ := root.HasString(rules, "7"); has {
		t.Error("HasString found the number 7 as a string")
	}
	if added, err := root.AddString(rules, "a"); added || err != nil {
		t.Errorf("AddString of a present string = %v, %v", added, err)
	}
	if removed, err := root.RemoveString(rules, "a"); !removed || err != nil {
		t.Errorf("RemoveString = %v, %v", removed, err)
	}
	if removed, _ := root.RemoveString(rules, "a"); removed {
		t.Error("RemoveString removed a string twice")
	}
	if added, err := root.AddString(rules, "c"); !added || err != nil {
		t.Errorf("AddString = %v, %v", added, err)
	}
	if _, err := adaptersetup.EditJSONFile(path, func(r *adaptersetup.Object) (bool, error) {
		r.Set("permissions", mustObject(t, root, "permissions"))
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, path), "{\n  \"permissions\": {\n    \"allow\": [\n      7,\n      \"b\",\n      null,\n      \"c\"\n    ]\n  }\n}\n"; got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}

	// Reading and removing make nothing; adding makes what is missing, in order.
	empty := adaptersetup.NewObject()
	if has, err := empty.HasString(rules, "a"); has || err != nil {
		t.Errorf("HasString on nothing = %v, %v", has, err)
	}
	if removed, err := empty.RemoveString(rules, "a"); removed || err != nil || len(empty.Keys()) != 0 {
		t.Errorf("RemoveString on nothing = %v, %v, keys %v", removed, err, empty.Keys())
	}
	if added, err := empty.AddString(rules, "a"); !added || err != nil {
		t.Errorf("AddString on nothing = %v, %v", added, err)
	}
	if o, err := empty.ObjectAt("permissions"); err != nil || len(o.Keys()) != 1 {
		t.Errorf("ObjectAt = %v, %v", o, err)
	}
	if _, err := empty.AddString(nil, "a"); err == nil {
		t.Error("an empty key path was accepted")
	}

	if _, err := wrongNumber.ObjectAt("x", "y"); !errors.Is(err, adaptersetup.ErrUnexpectedType) {
		t.Errorf("ObjectAt through a number = %v", err)
	}

	// A key that is missing below the wrong type does not make anything on the way.
	wrong := wrongNumber
	if _, err := wrong.AddString([]string{"x", "y", "z"}, "a"); !errors.Is(err, adaptersetup.ErrUnexpectedType) || len(wrong.Keys()) != 1 {
		t.Errorf("AddString through a number = %v, keys %v", err, wrong.Keys())
	}
}

func mustObject(t *testing.T, root *adaptersetup.Object, key string) *adaptersetup.Object {
	t.Helper()
	o, err := root.ObjectAt(key)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.json")
	write(t, path, content, 0o600)
	return path
}

// ReadJSONFile reads what EditJSONFile would edit, and writes nothing.
func TestReadJSONFile(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "settings.json")
	write(t, real, `{"a": [1]}`, 0o600)
	link := filepath.Join(dir, "settings.json")
	symlink(t, filepath.Join("dotfiles", "settings.json"), link)
	root, exists, err := adaptersetup.ReadJSONFile(link)
	if err != nil || !exists {
		t.Fatalf("ReadJSONFile = %v, %v", exists, err)
	}
	if _, ok := root.Get("a"); !ok {
		t.Errorf("keys = %v", root.Keys())
	}

	missing, exists, err := adaptersetup.ReadJSONFile(filepath.Join(dir, "nothing.json"))
	if err != nil || exists || len(missing.Keys()) != 0 {
		t.Errorf("a missing file = %v, %v, %v", missing, exists, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nothing.json")); err == nil {
		t.Error("reading made the file")
	}
	bad := writeTemp(t, `{"a": 1,}`)
	if _, _, err := adaptersetup.ReadJSONFile(bad); !errors.Is(err, adaptersetup.ErrInvalidJSON) {
		t.Errorf("an invalid file = %v", err)
	}
}
