package inproc

import (
	"context"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

const versionWarningInstance = "warning-probe"

func newVersionAlertOp() func(scenario.Env) (string, error) {
	// A cut may persist the enable before its caller gets the answer. Retain the
	// original version with its request ID so recovery replays that same request.
	var versionWarningRequest *recipes.ManagementRequest
	return func(e scenario.Env) (string, error) {
		h := e.(*harness.H)
		node := h.Engine()
		if versionWarningRequest == nil {
			instances, err := readManagementFile(e, config.InstancesPath, config.DecodeInstances)
			if err != nil {
				return "", err
			}
			versionWarningRequest = &recipes.ManagementRequest{ID: admin.OperationID{54}, Expected: instances.Version}
		}
		out, err := runManagement(e, node, *versionWarningRequest, "adapters.enable", versionWarningInstance+" "+versionWarningInstance,
			engine.EnableAdapterOp(versionWarningInstance, versionWarningInstance, nil, encodeManagementInstance))
		if err != nil {
			return "", err
		}
		if out.Code != "" || out.Value != "true" || out.Version != versionWarningRequest.Expected+1 {
			return "", fmt.Errorf("%w: warning adapter enable returned %+v", recipes.ErrManagementEffect, out)
		}
		ctx, cancel := context.WithTimeout(e.Ctx(), 10*time.Second)
		defer cancel()
		for {
			view, err := node.Adapter(ctx, versionWarningInstance)
			if err != nil {
				return "", err
			}
			if view.Probed {
				if !view.VersionUntested {
					return "", fmt.Errorf("%w: warning adapter's probe version is tested", recipes.ErrManagementEffect)
				}
				break
			}
			timer := clock.System().NewTimer(time.Millisecond)
			select {
			case <-timer.C():
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			}
		}
		if err := e.Quiesce(); err != nil {
			return "", err
		}
		scan, err := h.Records()
		if err != nil {
			return "", err
		}
		alerts := make([]recipes.VersionAlertFacts, 0, len(scan.Alerts))
		for _, alert := range scan.Alerts {
			alerts = append(alerts, recipes.VersionAlertFacts{ID: alert.ID.String(), Kind: alert.Kind, Subject: alert.Subject})
		}
		return recipes.RequireVersionAlert(alerts, versionWarningInstance)
	}
}
