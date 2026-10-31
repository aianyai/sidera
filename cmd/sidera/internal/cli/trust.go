package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
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

// decodeResult reads the result of a management response into v. An answer that
// is not what the method gives is an unknown outcome, as for every other command.
func decodeResult(raw []byte, v any) error {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || json.Unmarshal(envelope.Result, v) != nil {
		return apiwire.OutcomeUnknown("")
	}
	return nil
}

// awaitPairing is the end of `trust request`. A refusal by the target is a
// successful answer of the node and a failure of the command, with the exit code of
// a refusal. Otherwise the answer is printed at once, the code first, because the
// other node's owner needs it while this command waits; then, for at most wait,
// the command reads trust.list until the pairing is complete.
func (c *command) awaitPairing(ctx context.Context, client *manageapi.Client, raw []byte, wait time.Duration) error {
	var answer pairingAnswer
	if err := decodeResult(raw, &answer); err != nil {
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
		if _, err := fmt.Fprintf(c.Stdout, "Verification code: %s\nThe code is for information: trust is complete when both nodes expect each other (`sidera trust expect`).\nTo cancel a request that is stuck, run `sidera trust reject %s`.\n", answer.Code, answer.PairingID); err != nil {
			return err
		}
		if err := c.managementResult(raw); err != nil {
			return err
		}
	}
	if wait == 0 || answer.Status == "complete" {
		return c.pairedWith(answer)
	}
	poll := func(ctx context.Context) (trustView, error) {
		raw, err := client.Call(ctx, "trust.list", manageapi.Request{})
		if err != nil {
			return trustView{}, err
		}
		var view trustView
		return view, decodeResult(raw, &view)
	}
	if err := waitForPairing(ctx, answer, wait, poll, sleepContext); err != nil {
		return err
	}
	return c.pairedWith(answer)
}

// pairedWith says that the pairing is complete, when the request's answer or the
// wait showed it. A command that did not wait says nothing more.
func (c *command) pairedWith(answer pairingAnswer) error {
	if c.json || answer.Status != "complete" {
		return nil
	}
	_, err := fmt.Fprintf(c.Stdout, "paired with %s\n", answer.Node)
	return err
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

// trustList prints the view of trust in sections, one line for each entry; an
// empty section says none.
func (c *command) trustList(raw []byte) error {
	var view trustView
	if err := decodeResult(raw, &view); err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.Stdout, 0, 2, 2, ' ', 0)
	var rows [][]string
	for _, t := range view.Trusted {
		rows = append(rows, []string{t.Node, fmt.Sprint(t.Connected), t.CreatedAt, orNever(t.NotAfter), t.Credential})
	}
	section(w, "trusted", []string{"NODE", "CONNECTED", "CREATED_AT", "NOT_AFTER", "CREDENTIAL"}, rows)
	rows = nil
	for _, e := range view.Expectations {
		rows = append(rows, []string{e.Node, e.ExpiresAt})
	}
	section(w, "expectations", []string{"NODE", "EXPIRES_AT"}, rows)
	rows = nil
	for _, i := range view.Incoming {
		rows = append(rows, []string{i.PairingID, i.Node, i.Status, i.Code, fmt.Sprint(i.Expected), i.ExpiresAt})
	}
	section(w, "incoming", []string{"PAIRING_ID", "NODE", "STATUS", "CODE", "EXPECTED", "EXPIRES_AT"}, rows)
	rows = nil
	for _, o := range view.Outgoing {
		rows = append(rows, []string{o.PairingID, o.Node, o.Status, o.Code, o.Reason})
	}
	section(w, "outgoing", []string{"PAIRING_ID", "NODE", "STATUS", "CODE", "REASON"}, rows)
	rows = nil
	for _, n := range view.LocallyRevoked {
		rows = append(rows, []string{n})
	}
	section(w, "locally_revoked", []string{"NODE"}, rows)
	return w.Flush()
}

func orNever(t string) string {
	if t == "" {
		return "never"
	}
	return t
}

func section(w io.Writer, title string, header []string, rows [][]string) {
	fmt.Fprintln(w, title)
	if len(rows) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	fmt.Fprintln(w, "  "+strings.Join(header, "\t"))
	for _, row := range rows {
		fmt.Fprintln(w, "  "+strings.Join(row, "\t"))
	}
}
