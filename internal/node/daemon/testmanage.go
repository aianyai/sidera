//go:build crashtest

package daemon

import "github.com/aianyai/sidera/internal/node/manageapi"

func configureManagementTests(cfg *manageapi.Config) error {
	table, err := Table()
	if err != nil {
		return err
	}
	cfg.CrashPoints = table.Keys()
	return nil
}
