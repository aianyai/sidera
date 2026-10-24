package fsx

import (
	"errors"
	"fmt"
)

// Class says what a failed write left behind and so what the caller must do.
type Class uint8

const (
	// Failed: nothing durable changed. The caller rejects the request or
	// applies backpressure.
	Failed Class = iota + 1
	// Fatal: whether anything durable changed is unknown, or memory and disk
	// may disagree. The process must exit and rescan on restart.
	Fatal
)

func (c Class) String() string {
	switch c {
	case Failed:
		return "failed"
	case Fatal:
		return "fatal"
	}
	return fmt.Sprintf("class(%d)", uint8(c))
}

// Error is what a composite write operation returns on failure.
type Error struct {
	// Point is where the operation failed. Its ordinal is zero unless the FS
	// was wrapped by Observe, which is what numbers steps.
	Point Point
	// Path is the path the operation was working on.
	Path string
	// Class is decided only by the classification table in this package.
	Class Class
	// Err is the system error, for example ENOSPC or EIO.
	Err error
}

func (e *Error) Error() string {
	return fmt.Sprintf("fsx: %s %s (%s): %v", e.Point.Step, e.Path, e.Class, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// IsFatal reports whether err, or an error it wraps, is an *Error of class
// Fatal.
func IsFatal(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Class == Fatal
}

// stepError carries the point a step was numbered with. Observe wraps every
// error of a step in it; its text is the wrapped error's.
type stepError struct {
	point Point
	err   error
}

func (e *stepError) Error() string { return e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

// PointOf returns the point Observe numbered the failing step with, if err
// came through an observed FS.
func PointOf(err error) (Point, bool) {
	var e *stepError
	if errors.As(err, &e) {
		return e.point, true
	}
	return Point{}, false
}
