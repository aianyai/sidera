//go:build !acceptance || !diagnostic || !windows

package acceptance

import "github.com/aianyai/sidera/internal/node/testkit/fakectl"

type diagnosticWCLObserver struct{}

func (*diagnosticWCLObserver) enable()                             {}
func (*diagnosticWCLObserver) capture(fakectl.HelloParams, string) {}
func (*diagnosticWCLObserver) afterRemove(string, error)           {}
