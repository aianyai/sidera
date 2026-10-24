package fsxtest_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

func TestMemConformsToTheFSContract(t *testing.T) {
	fsxtest.RunConformance(t, func(*testing.T) fsx.FS { return fsxtest.NewMem() })
}
