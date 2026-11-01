//go:build !windows

package fakeruntime

import "os"

func readProcessInfoFile(path string) ([]byte, error) { return os.ReadFile(path) }
