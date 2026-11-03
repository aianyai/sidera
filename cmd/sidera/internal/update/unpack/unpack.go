// Package unpack extracts a release archive into an empty directory.
//
// The archive is untrusted input even after its checksum matched: the checksum
// proves who built it, not that Extract can safely write what is inside. So
// Extract writes only regular files and directories under the directory it is
// given, and refuses everything else.
package unpack

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrRefused marks an archive Extract will not unpack: a member that is not a
// plain relative path, not a regular file or directory, named twice, or over a
// limit, or a member whose size is not what the archive declares.
var ErrRefused = errors.New("unpack: archive refused")

// Limits bounds what Extract writes.
type Limits struct {
	MaxMember  int64 // largest single file, in bytes
	MaxTotal   int64 // all files together, in bytes
	MaxEntries int   // members of the archive, directories included
}

// DefaultLimits are many times what a release holds (a few dozen members, the
// largest a program of some 40 MiB) and small enough to bound a hostile archive.
func DefaultLimits() Limits {
	return Limits{MaxMember: 128 << 20, MaxTotal: 512 << 20, MaxEntries: 1024}
}

// Extract unpacks archive, a .tar.gz or a .zip chosen by its name, into dir.
// dir must exist and be empty, so every path below it is made by Extract and
// no link can lead out of it. File modes are not copied from the archive: a
// file with any executable bit becomes 0755, any other file 0644, a directory
// 0755 (the umask still applies).
//
// A refused or damaged archive stops Extract where it is found; what was
// written before stays, and the caller removes dir.
func Extract(archive, dir string, limits Limits) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("unpack: %s is not empty", dir)
	}
	e := &extractor{root: dir, limits: limits, seen: map[string]bool{}}
	switch {
	case strings.HasSuffix(archive, ".tar.gz"):
		return e.tarGz(archive)
	case strings.HasSuffix(archive, ".zip"):
		return e.zip(archive)
	}
	return fmt.Errorf("unpack: %s is neither a .tar.gz nor a .zip", filepath.Base(archive))
}

// extractor holds what the members read so far add up to.
type extractor struct {
	root   string
	limits Limits
	seen   map[string]bool // names of the members read, without a trailing slash
	total  int64
}

func refuse(name, reason string) error {
	return fmt.Errorf("%w: %q %s", ErrRefused, name, reason)
}

func (e *extractor) tarGz(archive string) error {
	f, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("unpack: reading %s: %w", filepath.Base(archive), err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = e.directory(h.Name)
		case tar.TypeReg:
			err = e.file(h.Name, h.FileInfo().Mode(), h.Size, tr)
		case tar.TypeSymlink, tar.TypeLink:
			err = refuse(h.Name, "is a link")
		default:
			err = refuse(h.Name, "is not a regular file or directory")
		}
		if err != nil {
			return err
		}
	}
}

func (e *extractor) zip(archive string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	defer zr.Close()
	// Refuse a huge member list before looking at any of its members.
	if len(zr.File) > e.limits.MaxEntries {
		return fmt.Errorf("%w: %d members, at most %d", ErrRefused, len(zr.File), e.limits.MaxEntries)
	}
	for _, zf := range zr.File {
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			err = e.directory(zf.Name)
		case mode.IsRegular():
			err = e.zipFile(zf)
		case mode&fs.ModeSymlink != 0:
			err = refuse(zf.Name, "is a link")
		default:
			err = refuse(zf.Name, "is not a regular file or directory")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *extractor) zipFile(zf *zip.File) error {
	if zf.UncompressedSize64 > uint64(e.limits.MaxMember) {
		return refuse(zf.Name, "is over the size limit of a file")
	}
	src, err := zf.Open()
	if err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	defer src.Close()
	return e.file(zf.Name, zf.Mode(), int64(zf.UncompressedSize64), src)
}

func (e *extractor) directory(name string) error {
	rel, err := e.admit(strings.TrimSuffix(name, "/"), name, 0)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.path(rel), 0o755); err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	return nil
}

// file writes src, which the archive declares to hold size bytes, as a new file.
func (e *extractor) file(name string, mode fs.FileMode, size int64, src io.Reader) error {
	rel, err := e.admit(name, name, size)
	if err != nil {
		return err
	}
	target := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	perm := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	// One byte more than declared shows a member that is longer than it says.
	n, err := io.Copy(out, io.LimitReader(src, size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("unpack: writing %s: %w", rel, err)
	}
	if n != size {
		return refuse(name, "is not the size the archive declares")
	}
	return nil
}

// admit counts one member against the limits and checks its name, which is bare
// (no trailing slash) in path; shown is the name as the archive spells it. It
// returns the slash-separated path below the root.
func (e *extractor) admit(path, shown string, size int64) (string, error) {
	if len(e.seen) >= e.limits.MaxEntries {
		return "", fmt.Errorf("%w: more than %d members", ErrRefused, e.limits.MaxEntries)
	}
	if !plainRelative(path) {
		return "", refuse(shown, "is not a plain relative path")
	}
	if e.seen[path] {
		return "", refuse(shown, "occurs twice")
	}
	if size > e.limits.MaxMember {
		return "", refuse(shown, "is over the size limit of a file")
	}
	if size > e.limits.MaxTotal-e.total {
		return "", refuse(shown, "takes the archive over its size limit")
	}
	e.seen[path] = true
	e.total += size
	return path, nil
}

func (e *extractor) path(rel string) string {
	return filepath.Join(e.root, filepath.FromSlash(rel))
}

// plainRelative reports whether name is made of non-empty components separated
// by "/", none of them "." or "..". A backslash, a colon (a drive) or a NUL byte
// anywhere refuses the name: they separate or qualify paths on Windows.
func plainRelative(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
