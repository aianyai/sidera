package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

const (
	// defaultPairingWait is how long `trust request` waits for the pairing to
	// complete unless --wait says otherwise.
	defaultPairingWait = 10 * time.Second
	// pairingPollInterval is how often the wait reads trust.list.
	pairingPollInterval = 500 * time.Millisecond
)

// pairingAnswer is the result of trust.request.
type pairingAnswer struct {
	PairingID string `json:"pairing_id"`
	Node      string `json:"node"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	Refusal   string `json:"refusal"`
}

type trustedEntry struct {
	Node       string `json:"node"`
	Credential string `json:"credential"`
	CreatedAt  string `json:"created_at"`
	NotAfter   string `json:"not_after"`
	Connected  bool   `json:"connected"`
	// Profile is what the node says about itself; absent when this node holds none.
	Profile *nodeProfile `json:"profile"`
}

type outgoingEntry struct {
	PairingID string `json:"pairing_id"`
	Node      string `json:"node"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	Reason    string `json:"reason"`
}

// trustView is the result of trust.list.
type trustView struct {
	Trusted      []trustedEntry `json:"trusted"`
	Expectations []struct {
		Node      string `json:"node"`
		ExpiresAt string `json:"expires_at"`
	} `json:"expectations"`
	Incoming []struct {
		PairingID string `json:"pairing_id"`
		Node      string `json:"node"`
		Status    string `json:"status"`
		Code      string `json:"code"`
		Expected  bool   `json:"expected"`
		ExpiresAt string `json:"expires_at"`
	} `json:"incoming"`
	Outgoing       []outgoingEntry `json:"outgoing"`
	LocallyRevoked []string        `json:"locally_revoked"`
}

// awaitPairing is the end of `trust request`. A refusal by the target is a
// successful answer of the node and a failure of the command, with the exit code of
// a refusal. Otherwise the answer is printed at once, the code first, because the
// other node's owner needs it while this command waits; then, for at most wait,
// the command reads the trust view through poll until the pairing is complete, and
// says so only when it is: by the answer itself or by the wait it ended on.
func (c *command) awaitPairing(ctx context.Context, poll func(context.Context) (trustView, error), raw []byte, wait time.Duration) error {
	envelope, err := manageapi.ReadAnswer(raw)
	if err != nil {
		return err
	}
	var answer pairingAnswer
	if err := envelope.DecodeResult(&answer); err != nil {
		return err
	}
	if answer.Status == "refused" {
		e := apiwire.New(apiwire.CategoryRefused, "pairing_refused", fmt.Sprintf("%s refused the request for a pairing (%s)", answer.Node, answer.Refusal))
		e.Details = map[string]any{"node": answer.Node, "refusal": answer.Refusal}
		return e
	}
	if c.json {
		if _, err := c.Stdout.Write(raw); err != nil {
			return err
		}
	} else {
		if _, err := io.WriteString(c.out, renderPairingRequested(c.out, answer, envelope.Replayed)); err != nil {
			return err
		}
	}
	complete := answer.Status == "complete"
	if !complete && wait > 0 {
		if err := waitForPairing(ctx, answer, wait, poll, sleepContext); err != nil {
			return err
		}
		complete = true
	}
	if c.json || !complete {
		return nil
	}
	_, err = io.WriteString(c.out, renderPaired(c.out, answer.Node))
	return err
}

// readTrustView returns the reader of the trust view that the wait of `trust request` polls.
func readTrustView(client *manageapi.Client) func(context.Context) (trustView, error) {
	return func(ctx context.Context) (trustView, error) {
		raw, err := client.Call(ctx, "trust.list", manageapi.Request{})
		if err != nil {
			return trustView{}, err
		}
		var view trustView
		return view, manageapi.DecodeResult(raw, &view)
	}
}

// waitForPairing reads the trust view, now and then every pairingPollInterval, until
// the pairing the answer names is complete: the node is trusted, or the request is
// complete. A request that ends any other way (the target rejected it, it
// expired, the owner withdrew it) stops the wait with that state. The time waited
// is counted as the intervals slept, so that tests need no clock; a read takes
// no time worth counting on a local socket.
func waitForPairing(ctx context.Context, answer pairingAnswer, wait time.Duration,
	poll func(context.Context) (trustView, error), sleep func(context.Context, time.Duration) error) error {
	for waited := time.Duration(0); ; waited += pairingPollInterval {
		view, err := poll(ctx)
		if err != nil {
			return err
		}
		complete, status := pairingProgress(view, answer)
		switch {
		case complete:
			return nil
		case status != "":
			return &failure{fmt.Sprintf("the pairing %s with %s (code %s) ended as %s; see `sidera trust list`", answer.PairingID, answer.Node, answer.Code, status)}
		case waited >= wait:
			return &failure{fmt.Sprintf("the pairing %s with %s (code %s) is not complete after %s; trust is complete when both nodes expect each other, so check `sidera trust list` on both, or cancel it with `sidera trust reject %s`",
				answer.PairingID, answer.Node, answer.Code, wait, answer.PairingID)}
		}
		if err := sleep(ctx, pairingPollInterval); err != nil {
			return err
		}
	}
}

// pairingProgress says whether the pairing is complete and, when it ended without
// completing, the state it ended in. A request that is still live (waiting for
// either owner), or one the view does not show, is neither.
func pairingProgress(view trustView, answer pairingAnswer) (complete bool, ended string) {
	for _, t := range view.Trusted {
		if t.Node == answer.Node {
			return true, ""
		}
	}
	for _, o := range view.Outgoing {
		if o.PairingID != answer.PairingID {
			continue
		}
		switch o.Status {
		case "complete":
			return true, ""
		case "awaiting_initiator_signature", "awaiting_target":
			return false, ""
		}
		return false, o.Status
	}
	return false, ""
}

// sleepContext waits for d or for the context to end.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// trustList prints the view of trust in sections (layouts in ops_output.go).
func (c *command) trustList(raw []byte) error {
	var view trustView
	if err := manageapi.DecodeResult(raw, &view); err != nil {
		return err
	}
	_, err := io.WriteString(c.out, renderTrust(c.out, view))
	return err
}
