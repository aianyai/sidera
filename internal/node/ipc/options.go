package ipc

import (
	"io/fs"
	"log/slog"
)

// Options tune Listen and Dial. The zero value is valid.
type Options struct {
	// Logger receives one line for every connection a listener turns away.
	// Nil discards them. Lines never contain request data.
	Logger *slog.Logger

	// files is where the directory and socket checks look. Nil means the real
	// file system below the data directory. Tests replace it to present a
	// directory that another user owns, which a test cannot create.
	files lstater
}

// lstater is the part of a read-only view of the data directory that the
// directory and socket checks use: Lstat of a slash-separated path relative to
// the data directory, never following a symbolic link.
type lstater interface {
	Lstat(name string) (fs.FileInfo, error)
}
