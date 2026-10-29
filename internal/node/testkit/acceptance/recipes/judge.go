package recipes

import (
	"fmt"

	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A returned identifier creates an obligation. A crash before the answer creates
// none here; the generic ledger judgements still apply to effects already seen.
func checkAcceptedBody(e scenario.Env, message adapterproto.MessageID, body string) error {
	if message == "" {
		return nil
	}
	log, err := e.Ledger()
	if err != nil {
		return err
	}
	for _, seen := range log.Kind(ledger.KindModelSaw) {
		if seen.Attrs.MessageID == string(message) && seen.Attrs.BodyDigest == ledger.Digest([]byte(body)) {
			return nil
		}
	}
	return fmt.Errorf("accepted message %s has no visible body or corresponding loss feedback", message)
}
