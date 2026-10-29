package recipes

import "fmt"

// VersionAlertFacts is an owner alert read from the current process's records.
// Bindings supply every owner alert, without filtering by the expected values.
type VersionAlertFacts struct {
	ID, Kind, Subject string
}

// RequireVersionAlert identifies the unique real warning for the enabled instance.
func RequireVersionAlert(alerts []VersionAlertFacts, instance string) (string, error) {
	if len(alerts) != 1 || alerts[0].ID == "" || alerts[0].Kind != "version_untested" || alerts[0].Subject != instance {
		return "", fmt.Errorf("%w: expected one version_untested alert for %s, got %+v", ErrManagementEffect, instance, alerts)
	}
	return alerts[0].ID, nil
}
