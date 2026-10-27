package engine

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/transport"
)

// What a target tells the caller of a call from another node is the caller's to
// read, but a failure that is neither a refusal nor a storage failure is the
// target's own defect, and only its own log says so: the caller sees the same
// error it sees for a target that cannot be reached.
func TestAFailureOfACallFromAnotherNodeThatIsNoRefusalIsLoggedAsAnError(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		wantLog bool
	}{
		{"a refusal", refuse(ReasonMessageConflict), false},
		{"a refusal wrapped", fmt.Errorf("judging: %w", refuse(ReasonClockRollback)), false},
		{"a full disk", fmt.Errorf("writing: %w", fsxtest.DiskFull), false},
		{"an error that is none of those", errors.New("something nobody foresaw"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			e := &Engine{log: slog.New(slog.NewTextHandler(&buf, nil))}
			got := e.classifyPeerCall("inbound", transport.RemoteOrigin(ident.NodeID{}), c.err)
			if got == nil {
				t.Fatal("the error was lost")
			}
			if logged := strings.Contains(buf.String(), "level=ERROR"); logged != c.wantLog {
				t.Errorf("logged at error: %v, want %v (log %q)", logged, c.wantLog, buf.String())
			}
		})
	}
}

// Both entries of the receiving side answer through that rule: a node with no keys
// fails with an error that is no refusal.
func TestTheEntriesOfTheReceivingSideLogWhatIsNoRefusal(t *testing.T) {
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	for name, run := range map[string]func(e *Engine) error{
		"inbound": func(e *Engine) error {
			c := &cmdInbound{call: newCall[transport.Receipt]()}
			c.run(e, now)
			return (<-c.ch).err
		},
		"pull": func(e *Engine) error {
			c := &cmdPull{call: newCall[transport.PullAnswer]()}
			c.run(e, now)
			return (<-c.ch).err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			err := run(&Engine{log: slog.New(slog.NewTextHandler(&buf, nil))})
			if !errors.Is(err, errNoKeys) {
				t.Fatalf("the call failed with %v, want the error of a node with no keys", err)
			}
			if !strings.Contains(buf.String(), "level=ERROR") {
				t.Errorf("nothing was logged at error: %q", buf.String())
			}
		})
	}
}
