package disk

import (
	"fmt"

	"github.com/aianyai/sidera/internal/node/store"
)

// CheckClosedSession requires a retained closed endpoint with no token or checkpoint.
func CheckClosedSession(scan *store.Scan, endpoint string) error {
	found := false
	for _, ep := range scan.Endpoints {
		if ep.ID.String() == endpoint {
			found = true
			if ep.Status != store.EndpointClosed || len(ep.Token) != 0 {
				return fmt.Errorf("session %s is not closed with an empty token", endpoint)
			}
		}
	}
	if !found {
		return fmt.Errorf("closed session %s is missing before retention", endpoint)
	}
	for _, cp := range scan.Checkpoints {
		if cp.Key.Scope == store.CheckpointScopeSession && cp.Key.ID == endpoint {
			return fmt.Errorf("closed session %s retains a checkpoint", endpoint)
		}
	}
	return nil
}
