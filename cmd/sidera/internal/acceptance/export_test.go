//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func BindCatalogForTest() ([]crashwalk.Scenario, []crashwalk.RecoveryTrigger) {
	return bindCatalog()
}

func WalkBackendForTest(t *testing.T) crashwalk.Backend {
	t.Helper()
	return NewWalkBackend(runRoot(t), testBinaries, Options{Seed: t.Name()})
}
