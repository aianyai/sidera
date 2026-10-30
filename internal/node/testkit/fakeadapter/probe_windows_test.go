//go:build windows

package fakeadapter_test

import "syscall"

const probeConnectionReset = syscall.WSAECONNRESET
