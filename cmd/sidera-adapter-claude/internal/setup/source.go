package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
	"github.com/aianyai/sidera/sdk/go/guidance"
)

// sourceFile is one file of the plugin's source directory, with its path below it
// in slash form.
type sourceFile struct {
	path    string
	content []byte
}

// sourceDir is where the plugin's source lives: a marketplace directory of its own in
// the adapter's state directory. Claude Code never writes into it (it loads the
// plugin from it in place, and keeps its copies in its own configuration directory),
// which is why the daemon's checks of the state directory are not disturbed.
func sourceDir(stateDir string) string {
	return filepath.Join(stateDir, "setup", "claude", "marketplace")
}

const pluginDescription = "Tells an agent what the Sidera message channel is and how to use it."

// sourceFiles are the files of the marketplace and its one plugin, which holds the
// Skill and nothing else (the tools come from the server registered in the user's
// scope, so they are named mcp__sidera__* and one rule allows them). The plugin's
// version is the adapter's, so a new adapter makes the installed copy stale.
func sourceFiles(version string) ([]sourceFile, error) {
	marketplace := map[string]any{
		"name":  marketplaceName,
		"owner": map[string]any{"name": "Sidera"},
		"plugins": []any{map[string]any{
			"name": pluginName, "source": "./plugins/" + pluginName, "description": pluginDescription,
		}},
	}
	plugin := map[string]any{"name": pluginName, "version": version, "description": pluginDescription}
	var files []sourceFile
	for _, f := range []struct {
		path  string
		value any
	}{
		{".claude-plugin/marketplace.json", marketplace},
		{"plugins/" + pluginName + "/.claude-plugin/plugin.json", plugin},
	} {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(f.value); err != nil {
			return nil, err
		}
		files = append(files, sourceFile{f.path, b.Bytes()})
	}
	files = append(files, sourceFile{"plugins/" + pluginName + "/skills/" + guidance.SkillName + "/SKILL.md", guidance.Skill()})
	return files, nil
}

// digestOfSource is the Digest of the files: their paths and contents in order, so
// that a missing file, an added one and a changed one all differ.
func digestOfSource(files []sourceFile) string {
	var b bytes.Buffer
	for _, f := range files {
		fmt.Fprintf(&b, "%s\x00%d\x00", f.path, len(f.content))
		b.Write(f.content)
	}
	return adaptersetup.Digest(b.Bytes())
}

// readSource returns the Digest of what the directory holds at the paths of files
// (a file that is not there counts as empty and differs from any wanted one), and
// whether the directory exists.
func readSource(dir string, files []sourceFile) (digest string, exists bool, err error) {
	if info, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	} else if !info.IsDir() {
		return "", false, fmt.Errorf("%s is not a directory", dir)
	}
	have := make([]sourceFile, len(files))
	for i, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.path)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
		have[i] = sourceFile{f.path, data}
	}
	return digestOfSource(have), true, nil
}

// writeSource writes the files for the owner alone; each is replaced atomically, so a
// session starting meanwhile reads a whole file.
func writeSource(dir string, files []sourceFile) error {
	for _, f := range files {
		if err := adaptersetup.WritePrivateFile(filepath.Join(dir, filepath.FromSlash(f.path)), f.content); err != nil {
			return err
		}
	}
	return nil
}

// removeSource deletes the source directory, and the directory above it if that is
// left empty.
func removeSource(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	_ = os.Remove(filepath.Dir(dir))
	return nil
}
