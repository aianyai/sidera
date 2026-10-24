// Package node is the parent of the daemon's runtime packages: the code that
// does I/O, runs goroutines and reads the clock. It holds no code of its own.
//
// The packages under internal/ other than this tree are contract packages and
// stay pure, so they never import anything below internal/node. The tests in
// this directory enforce that boundary and the three rules runtime packages
// follow: time comes only from the injected clock, files are touched only
// through the file-system layer, and randomness arrives as an io.Reader. The
// package exists so those tests have a home inside the tree they scan.
package node
