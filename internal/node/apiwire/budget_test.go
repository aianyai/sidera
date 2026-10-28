package apiwire_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/apiwire"
)

func TestBudgetUnitsHaveTheirExistingWireNames(t *testing.T) {
	for _, tc := range []struct {
		unit handoff.Unit
		name string
	}{
		{handoff.UnitChars, "chars"},
		{handoff.UnitTokens, "tokens"},
	} {
		got, err := apiwire.BudgetUnitName(tc.unit)
		if err != nil || got != tc.name {
			t.Errorf("BudgetUnitName(%d) = %q, %v; want %q", tc.unit, got, err, tc.name)
		}
	}
	if _, err := apiwire.BudgetUnitName(0); err == nil || apiwire.FromError(err).Category != apiwire.CategoryInternal {
		t.Errorf("unknown unit = %v, want an internal-mapped error", err)
	}
}
