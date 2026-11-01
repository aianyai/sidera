package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Encode returns a manifest as the text of sidera-adapter.json: validated, indented
// and ending in a newline.
func Encode(m Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WritePackage writes the adapter package of m into dir: the manifest, and a copy
// of binary at the manifest's entry program, so that the file the installer looks
// for is the file that was written. A package holds one binary, so for one built
// for Windows (a single platform whose os is windows) the program gets the
// extension `.exe` if the manifest names it without; Windows starts no file
// without an executable extension, and every adapter's manifest therefore names
// the program the same way on every platform. The manifest written carries the
// program as it was written. Nothing is written when the manifest is invalid or
// the binary cannot be opened; the manifest is written last, so a package that was
// cut short has no manifest to install from. Files already in dir are kept, and
// those with the same names are replaced: refusing a directory that is not empty is
// for the caller.
func WritePackage(dir string, m Manifest, binary string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if len(m.Platforms) == 1 && m.Platforms[0].OS == "windows" && !strings.HasSuffix(strings.ToLower(m.Entry.Program), ".exe") {
		m.Entry.Program += ".exe"
	}
	raw, err := Encode(m)
	if err != nil {
		return err
	}
	src, err := os.Open(binary)
	if err != nil {
		return err
	}
	defer src.Close()
	entry := filepath.Join(dir, filepath.FromSlash(m.Entry.Program))
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		return err
	}
	dst, err := os.OpenFile(entry, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("copy %s: %w", binary, err)
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, FileName), raw, 0o644)
}
