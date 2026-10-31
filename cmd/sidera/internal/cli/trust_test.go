package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
)

// What each command of trust sends: the method, the node, the addresses only when
// given, and the wait that stays on the client's side. A command with the wrong
// number of arguments, no node, or a wait that is not a duration is a usage error.
func TestTrustCommandsPlanTheirMethods(t *testing.T) {
	const node = "node1abc"
	const hint = "/ip4/10.0.0.1/tcp/7744"
	for _, tc := range []struct {
		args    []string
		method  string
		params  map[string]any
		wait    time.Duration
		changes bool
		usage   bool
	}{
		{args: []string{"trust", "expect", node}, method: "trust.expect", params: map[string]any{"node": node}, changes: true},
		{args: []string{"trust", "forget", node}, method: "trust.forget", params: map[string]any{"node": node}, changes: true},
		{args: []string{"trust", "reject", "ab12"}, method: "trust.reject", params: map[string]any{"pairing_id": "ab12"}, changes: true},
		{args: []string{"trust", "reject"}, usage: true},
		{args: []string{"trust", "remove", node}, method: "trust.remove", params: map[string]any{"node": node}, changes: true},
		{args: []string{"trust", "clear", node}, method: "trust.clear", params: map[string]any{"node": node}, changes: true},
		{args: []string{"trust", "list"}, method: "trust.list"},
		{args: []string{"trust", "request", node}, method: "trust.request", params: map[string]any{"node": node}, wait: defaultPairingWait, changes: true},
		{args: []string{"trust", "request", node, hint, "--wait", "30s"}, method: "trust.request",
			params: map[string]any{"node": node, "addresses": []string{hint}}, wait: 30 * time.Second, changes: true},
		{args: []string{"trust", "request", "--wait=0", node}, method: "trust.request", params: map[string]any{"node": node}, changes: true},
		{args: []string{"trust", "request"}, usage: true},
		{args: []string{"trust", "request", node, "--wait", "10"}, usage: true},
		{args: []string{"trust", "request", node, "--wait", "-1s"}, usage: true},
		{args: []string{"trust", "expect"}, usage: true},
		{args: []string{"trust", "expect", node, "extra"}, usage: true},
		{args: []string{"trust", "list", "extra"}, usage: true},
		{args: []string{"trust"}, usage: true},
		{args: []string{"trust", "revoke", node}, usage: true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			c := command{Config: Config{Getenv: func(string) string { return "" }}}
			c.arguments = arguments(tc.args)
			rest, err := c.globals(tc.args, c.arguments.flags)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := c.managementPlan(rest)
			var u *usageError
			if tc.usage {
				if !errors.As(err, &u) {
					t.Fatalf("error %v, want a usage error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.method != tc.method || plan.change != tc.changes || plan.wait != tc.wait || !reflect.DeepEqual(plan.params, orEmpty(tc.params)) {
				t.Errorf("plan %+v, want %s with %v and wait %s", plan, tc.method, tc.params, tc.wait)
			}
			if plan.versionMethod != "" {
				t.Errorf("a command of trust reads a version from %q; none carries one", plan.versionMethod)
			}
		})
	}
}

// The wait reads the view at once and then every interval, counting only the
// intervals it sleeps: it stops when the node is trusted or the request is complete,
// fails with the state of a request that ended, and gives up when the wait is spent,
// naming what the owner does next.
func TestWaitingForAPairing(t *testing.T) {
	answer := pairingAnswer{PairingID: "ab12", Node: "node1x", Status: "awaiting_target", Code: "482 019"}
	live := trustView{Outgoing: []outgoingEntry{{PairingID: "ab12", Status: "awaiting_target"}}}
	complete := trustView{Outgoing: []outgoingEntry{{PairingID: "ab12", Status: "complete"}}}
	trusted := trustView{Trusted: []trustedEntry{{Node: "node1x"}}}
	rejected := trustView{Outgoing: []outgoingEntry{{PairingID: "other", Status: "complete"}, {PairingID: "ab12", Status: "rejected"}}}

	for _, tc := range []struct {
		name    string
		views   []trustView // the view each read answers; the last repeats
		wait    time.Duration
		reads   int
		slept   time.Duration
		wantErr string // a word of the failure; empty for none
	}{
		{"complete at once", []trustView{complete}, 10 * time.Second, 1, 0, ""},
		{"complete after two reads", []trustView{live, live, complete}, 10 * time.Second, 3, 2 * pairingPollInterval, ""},
		{"the node is trusted", []trustView{live, trusted}, 10 * time.Second, 2, pairingPollInterval, ""},
		{"the request ended", []trustView{live, rejected}, 10 * time.Second, 2, pairingPollInterval, "ended as rejected"},
		{"the wait runs out", []trustView{live}, 2 * time.Second, 5, 4 * pairingPollInterval, "not complete after 2s"},
		{"a wait shorter than an interval reads twice", []trustView{live}, time.Millisecond, 2, pairingPollInterval, "not complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads int
			var slept time.Duration
			poll := func(context.Context) (trustView, error) {
				v := tc.views[min(reads, len(tc.views)-1)]
				reads++
				return v, nil
			}
			sleep := func(_ context.Context, d time.Duration) error { slept += d; return nil }
			err := waitForPairing(context.Background(), answer, tc.wait, poll, sleep)
			if reads != tc.reads || slept != tc.slept {
				t.Errorf("%d reads and %s slept, want %d and %s", reads, slept, tc.reads, tc.slept)
			}
			var f *failure
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("error %v", err)
			case tc.wantErr != "" && !errors.As(err, &f):
				t.Fatalf("error %v, want a failure", err)
			case tc.wantErr != "":
				for _, word := range []string{tc.wantErr, "ab12", "node1x"} {
					if !strings.Contains(f.message, word) {
						t.Errorf("the failure %q does not say %q", f.message, word)
					}
				}
				if tc.wait > pairingPollInterval && strings.Contains(tc.wantErr, "not complete") && !strings.Contains(f.message, "482 019") {
					t.Errorf("the failure %q does not give the code", f.message)
				}
			}
		})
	}

	// A read that fails, or a context that ends while sleeping, ends the wait with
	// that error.
	boom := errors.New("boom")
	err := waitForPairing(context.Background(), answer, time.Minute, func(context.Context) (trustView, error) { return trustView{}, boom },
		func(context.Context, time.Duration) error { return nil })
	if !errors.Is(err, boom) {
		t.Errorf("a failed read: %v", err)
	}
	err = waitForPairing(context.Background(), answer, time.Minute, func(context.Context) (trustView, error) { return live, nil },
		func(context.Context, time.Duration) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a wait that is interrupted: %v", err)
	}
}

// trust list in words: every section is there, one line for each entry, and an empty
// one says none.
func TestTrustListIsPrintedInSections(t *testing.T) {
	var out bytes.Buffer
	c := command{Config: Config{Stdout: nopCloser{&out}}}
	empty := `{"result":{"trusted":[],"expectations":[],"incoming":[],"outgoing":[],"locally_revoked":[]}}`
	if err := c.trustList([]byte(empty)); err != nil {
		t.Fatal(err)
	}
	if want := "trusted\n  none\nexpectations\n  none\nincoming\n  none\noutgoing\n  none\nlocally_revoked\n  none\n"; out.String() != want {
		t.Errorf("an empty view:\n%s\nwant\n%s", out.String(), want)
	}

	out.Reset()
	full := `{"result":{"trusted":[{"node":"n1","credential":"c1","created_at":"2026-10-24T09:00:00Z","connected":true}],` +
		`"expectations":[{"node":"n2","expires_at":"2026-10-24T10:00:00Z"}],` +
		`"incoming":[{"pairing_id":"p1","node":"n3","status":"awaiting_target","code":"111 222","expected":true,"expires_at":"2026-10-24T09:05:00Z"}],` +
		`"outgoing":[{"pairing_id":"p2","node":"n4","status":"withdrawn","code":"333 444","reason":"declined"}],` +
		`"locally_revoked":["n5"]}}`
	if err := c.trustList([]byte(full)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"n1  true  2026-10-24T09:00:00Z  never  c1",
		"n2  2026-10-24T10:00:00Z",
		"p1  n3  awaiting_target  111 222  true  2026-10-24T09:05:00Z",
		"p2  n4  withdrawn  333 444  declined",
		"  n5\n",
	} {
		if !strings.Contains(squeeze(out.String()), want) {
			t.Errorf("the list does not have %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "none") {
		t.Errorf("a section with entries says none:\n%s", out.String())
	}
}

// squeeze makes the runs of spaces that align columns two spaces wide.
func squeeze(s string) string {
	for strings.Contains(s, "   ") {
		s = strings.ReplaceAll(s, "   ", "  ")
	}
	return s
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// A request the target refused ends the command with the exit code of a refusal and
// prints no code; one that is answered prints the code before anything else, says
// that trust is complete when both nodes expect each other and how to cancel the
// request, and with no wait the command ends there.
func TestTheAnswerToARequestForAPairing(t *testing.T) {
	var out bytes.Buffer
	c := command{Config: Config{Stdout: nopCloser{&out}}}
	err := c.awaitPairing(context.Background(), nil,
		[]byte(`{"operation_id":"00000000000000000000000000000001","replayed":false,"result":{"pairing_id":"","node":"n1","status":"refused","code":"","refusal":"busy"}}`), time.Minute)
	if code := apiwire.FromError(err).Category.ExitCode(); code != 7 || out.Len() != 0 {
		t.Errorf("a refusal: %v, exit %d, printed %q", err, code, out.String())
	}
	err = c.awaitPairing(context.Background(), nil,
		[]byte(`{"operation_id":"00000000000000000000000000000001","replayed":false,"result":{"pairing_id":"p1","node":"n1","status":"awaiting_target","code":"482 019"}}`), 0)
	if err != nil || !strings.HasPrefix(out.String(), "Verification code: 482 019\n") || !strings.Contains(out.String(), "`sidera trust reject p1`") {
		t.Errorf("an answer: %v, printed %q", err, out.String())
	}
}
