package apiwire

import (
	"fmt"

	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// BudgetUnitName is the existing wire name of an output-budget unit. The core
// unit remains a value for calculations; formatting it implicitly would change
// logs and diagnostics that are not part of the interface protocol.
func BudgetUnitName(unit handoff.Unit) (string, error) {
	switch unit {
	case handoff.UnitChars:
		return string(manifest.UnitChars), nil
	case handoff.UnitTokens:
		return string(manifest.UnitTokens), nil
	default:
		return "", fmt.Errorf("apiwire: unknown output budget unit %d", unit)
	}
}
