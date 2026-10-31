package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// of binary at program, a slash-separated path inside the package. It is the entry
// program of the manifest, except where the platform adds an extension to the file
// that the manifest leaves out. Nothing is written when the manifest is invalid or
// the binary cannot be opened; the manifest is written last, so a package that was
// cut short has no manifest to install from. Files already in dir are kept, and
// those with the same names are replaced: refusing a directory that is not empty is
// for the caller.
func WritePackage(dir string, m Manifest, binary, program string) error {
	raw, err := Encode(m)
	if err != nil {
		return err
	}
	src, err := os.Open(binary)
	if err != nil {
		return err
	}
	defer src.Close()
	entry := filepath.Join(dir, filepath.FromSlash(program))
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
