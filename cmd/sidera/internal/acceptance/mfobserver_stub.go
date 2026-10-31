//go:build !diagnostic

package acceptance

import "github.com/aianyai/sidera/internal/node/testkit/crashwalk"

func mfObserveCheck(crashwalk.RunPlan, string, *environment) {}
