package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
)

// NotPrivateError says which path of a tree someone other than the owner can
// reach, and how. It is what CheckPrivateTree returns for an exposure, as opposed
// to a failure to examine the tree.
type NotPrivateError struct {
	// Path is the path relative to the root of the Reader, "." for the root.
	Path string
	// Detail follows the path in a sentence: Info.Exposure, or what is wrong with
	// the kind of object. It is never empty.
	Detail string
}

func (e *NotPrivateError) Error() string { return e.Path + " " + e.Detail }

// CheckPrivateTree refuses a tree in which someone other than the owner can reach
// what matters, and it fails closed. It judges:
//
//   - dir and every directory below it, whatever they hold;
//   - every regular file for which judgeFile is true, by that file's own access
//     list: a file made in a private directory inherits that directory's list,
//     but an entry added to the file itself, or a file moved in from elsewhere,
//     is a list the directory does not show. The caller names the files that hold
//     a secret; the rest rely on their directory, which keeps the cost
//     proportionate to a tree of tens of thousands of record files, each of which
//     would otherwise cost a read of its access list;
//   - a symbolic link for which judgeFile is true, which is refused unless it
//     resolves to a socket private to its owner, in a directory private to its
//     owner (Reader.ResolveLink): a link is not a file, and ReadFile follows it to
//     a file nobody here has judged, but a runtime that cannot bind a long socket
//     path links it to a short private one, and a socket is not read. A link to
//     a file, a directory, nothing, or a socket others can reach is refused.
//
// A socket, a device or a junction among the entries is left to the directory
// that holds it. The first exposure found is returned as a *NotPrivateError; any
// other failure to examine wraps the file system's error. An entry that vanishes
// between the listing and its look, as the temporary file of a running daemon
// does, is skipped: it is not an exposure. Nothing is changed.
func CheckPrivateTree(r Reader, dir string, judgeFile func(rel string) bool) error {
	info, err := r.Stat(dir)
	if err != nil {
		return fmt.Errorf("fsx: examining %s: %w", dir, err)
	}
	return checkPrivateDir(r, dir, info, judgeFile)
}

func checkPrivateDir(r Reader, dir string, info Info, judgeFile func(string) bool) error {
	switch {
	case !info.IsDir:
		return &NotPrivateError{dir, "is not a directory (a symbolic link is not accepted)"}
	case !info.OwnerOnly:
		return exposed(dir, info)
	}
	entries, err := r.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("fsx: listing %s: %w", dir, err)
	}
	for _, e := range entries {
		p := path.Join(dir, e.Name)
		var err error
		switch {
		case e.IsDir:
			err = checkPrivateEntry(r, p, func(info Info) error { return checkPrivateDir(r, p, info, judgeFile) })
		case e.IsSymlink && judgeFile(p):
			err = checkPrivateLink(r, p)
		case e.IsRegular && judgeFile(p):
			err = checkPrivateEntry(r, p, func(info Info) error {
				if !info.OwnerOnly {
					return exposed(p, info)
				}
				return nil
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// checkPrivateLink accepts a symbolic link only to a socket private to its owner
// in a directory private to its owner, and refuses every other link, which is
// what a file that holds a secret must not be.
func checkPrivateLink(r Reader, p string) error {
	refused := &NotPrivateError{p, "is a symbolic link, and a file that holds a secret is not one"}
	t, err := r.ResolveLink(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return refused
	case err != nil:
		return fmt.Errorf("fsx: examining %s: %w", p, err)
	case !t.IsSocket:
		return refused
	case t.Exposure != "":
		return &NotPrivateError{p, fmt.Sprintf("is a symbolic link to %s, and that target %s", t.Path, t.Exposure)}
	case t.ParentExposure != "":
		return &NotPrivateError{p, fmt.Sprintf("is a symbolic link to %s, whose directory %s %s", t.Path, t.Parent, t.ParentExposure)}
	}
	return nil
}

// checkPrivateEntry looks at an entry that the listing named and passes what it
// finds to judge, and has nothing to judge when the entry is gone.
func checkPrivateEntry(r Reader, p string, judge func(Info) error) error {
	info, err := r.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("fsx: examining %s: %w", p, err)
	}
	return judge(info)
}

func exposed(p string, info Info) error {
	detail := info.Exposure
	if detail == "" {
		detail = "can be reached by others; only its owner may"
	}
	return &NotPrivateError{p, detail}
}
