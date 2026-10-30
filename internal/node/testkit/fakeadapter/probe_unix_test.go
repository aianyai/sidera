//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package fakeadapter_test

import "syscall"

const probeConnectionReset = syscall.ECONNRESET
