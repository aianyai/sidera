package disk_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
)

func TestInspectDamageCountsActualOwnerAlerts(t *testing.T) {
	damage := disk.DamageResult{Endpoint: "creating-endpoint", Body: "state/bodies/damaged.body"}
	for _, test := range []struct {
		name   string
		alerts []store.AlertRecord
		want   recipes.DamageFacts
	}{
		{
			name: "no alerts",
			want: recipes.DamageFacts{Applied: true, Endpoint: damage.Endpoint, Body: damage.Body},
		},
		{
			name: "all owner alerts and only matching corrupt bodies",
			alerts: []store.AlertRecord{
				{Kind: "control_unknown", Subject: "another-endpoint"},
				{Kind: "control_unknown", Subject: damage.Endpoint},
				{Kind: "corrupt_body", Subject: damage.Body},
				{Kind: "corrupt_body", Subject: damage.Body},
				{Kind: "corrupt_body", Subject: "another-body"},
				{Kind: "quarantine", Subject: damage.Body},
			},
			want: recipes.DamageFacts{Applied: true, Endpoint: damage.Endpoint, Body: damage.Body, OwnerAlerts: 6, ControlUnknownAlerts: 2, CorruptBodyAlerts: 2},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := disk.InspectDamage(&store.Scan{Alerts: test.alerts}, damage)
			if got != test.want {
				t.Fatalf("damage facts = %+v, want %+v", got, test.want)
			}
		})
	}
}
