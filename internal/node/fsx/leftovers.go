package fsx

import (
	"path"
	"strings"
)

// IsLeftover reports whether a directory entry name marks the debris of an
// interrupted operation: a temporary file or a discarded directory.
func IsLeftover(name string) bool {
	return strings.HasPrefix(name, TempPrefix) || strings.HasPrefix(name, TrashPrefix)
}

// Leftover is one debris entry found by Leftovers.
type Leftover struct {
	Path  string // relative to the root
	IsDir bool
}

// Leftovers lists every temporary file and discarded directory under dir,
// recursively, in a stable order (a directory's entries sorted by name, each
// directory before what it contains). It does not descend into a leftover
// directory: removing the directory removes what is inside.
//
// It only reads. Removing is a write and goes through the caller's
// transaction, so each removal carries a tag.
func Leftovers(r Reader, dir string) ([]Leftover, error) {
	var found []Leftover
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := r.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			p := path.Join(dir, e.Name)
			switch {
			case IsLeftover(e.Name):
				found = append(found, Leftover{Path: p, IsDir: e.IsDir})
			case e.IsDir:
				if err := walk(p); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(dir); err != nil {
		return nil, err
	}
	return found, nil
}
