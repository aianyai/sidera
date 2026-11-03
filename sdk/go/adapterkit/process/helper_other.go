//go:build !windows

package process

import "io"

// There is no helper off Windows; HandleHelperInvocation returns before this.
func runHelper([]string, io.Writer, io.Writer) int { return 1 }
