package recipes_test

import (
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
)

func TestVersionAlertRequiresExactOwnerEvidence(t *testing.T) {
	const instance = "warning-probe"
	correct := recipes.VersionAlertFacts{ID: "actual-alert", Kind: "version_untested", Subject: instance}
	for _, tt := range []struct {
		name   string
		alerts []recipes.VersionAlertFacts
	}{
		{"absent", nil},
		{"extra", []recipes.VersionAlertFacts{correct, correct}},
		{"wrong-kind", []recipes.VersionAlertFacts{{ID: correct.ID, Kind: "control_unknown", Subject: instance}}},
		{"wrong-subject", []recipes.VersionAlertFacts{{ID: correct.ID, Kind: correct.Kind, Subject: "other-instance"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := recipes.RequireVersionAlert(tt.alerts, instance); !errors.Is(err, recipes.ErrManagementEffect) {
				t.Fatalf("invalid owner evidence accepted: %v", err)
			}
		})
	}
	if id, err := recipes.RequireVersionAlert([]recipes.VersionAlertFacts{correct}, instance); err != nil || id != correct.ID {
		t.Fatalf("real warning rejected: id=%s err=%v", id, err)
	}
}
