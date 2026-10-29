package inproc

import (
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func newDeliveryOps() recipes.DeliveryOps {
	return recipes.DeliveryOps{FailSourceUpdate: func(e scenario.Env) error {
		// This recipe's first source update follows its first send. A failed create
		// is before any visible replacement, so the engine keeps serving and retries.
		e.(*harness.H).Faults().Fail(fsxtest.Match{Tx: "source-update", Role: "out", Step: fsx.StepCreate, Hit: 1}, fsxtest.DiskFull)
		return nil
	}}
}
