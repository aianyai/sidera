//go:build !crashtest

package daemon

import "github.com/aianyai/sidera/internal/node/manageapi"

func configureManagementTests(*manageapi.Config) error { return nil }
