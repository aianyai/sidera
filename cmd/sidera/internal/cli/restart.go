package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

const (
	// defaultRestartWait is how long `daemon restart` waits for the new daemon unless
	// --wait says otherwise: the old daemon shuts down in an orderly way, which may
	// take its adapters' shutdown grace, and the new one then recovers its state.
	defaultRestartWait = 2 * time.Minute
	// restartPollInterval is how often the wait reads status.
	restartPollInterval = 200 * time.Millisecond
)

// caller is the part of the management client that a command which calls more
// than once needs, so that tests can answer in its place.
type caller interface {
	Call(ctx context.Context, method string, in manageapi.Request) (json.RawMessage, error)
}

// restartRequest is what a restart of the daemon is asked with.
type restartRequest struct {
	// ExpectVersion, when not empty, is the version the program that is started must
	// report, or the daemon refuses to restart.
	ExpectVersion string
	// Wait is how long to wait for the new daemon after the daemon has accepted the
	// restart; zero returns at once.
	Wait time.Duration
	// Home is the data directory, for the words of a wait that runs out.
	Home string
	// Sleep waits between two reads of status; nil sleeps for the interval.
	Sleep func(context.Context, time.Duration) error
}

// restartResult is what is known of a restart: the answer of the daemon that went
// away and, once the wait has seen it, the daemon that took its place.
type restartResult struct {
	Restarting      bool
	Program         string
	Version         string // what the program reported to the check before the restart
	PreviousStartID string
	Running         bool
	StartID         string // of the new daemon; set with Running
	OperationID     string
	Replayed        bool
}

// restartDaemon is the one place that asks the daemon to restart and waits for the
// new one. The request is chosen once and, when its outcome is unknown, sent again
// as it was (the daemon answers a resend with the stored result and does not
// restart a second time). announce, when not nil, is called with the answer before
// the wait begins.
func restartDaemon(ctx context.Context, node caller, random io.Reader, req restartRequest, announce func(restartResult) error) (restartResult, error) {
	id, err := admin.NewOperationID(random)
	if err != nil {
		return restartResult{}, err
	}
	params := map[string]any{}
	if req.ExpectVersion != "" {
		params["expect_version"] = req.ExpectVersion
	}
	raw, err := callResending(ctx, node, "daemon.restart", manageapi.Request{OperationID: id, Params: params})
	if err != nil {
		if apiwire.FromError(err).Code == apiwire.CodeMethodUnknown {
			// A daemon from before 0.12.0 has no such method and cannot be asked to restart.
			return restartResult{}, &failure{"the running daemon predates `daemon restart` and cannot be restarted this way: " +
				"stop it with `sidera daemon stop` and start it the way it was started"}
		}
		return restartResult{}, err
	}
	var answer struct {
		OperationID string `json:"operation_id"`
		Replayed    bool   `json:"replayed"`
		Result      struct {
			Restarting bool   `json:"restarting"`
			Program    string `json:"program"`
			Version    string `json:"version"`
			StartID    string `json:"start_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return restartResult{}, apiwire.OutcomeUnknown("")
	}
	res := restartResult{
		Restarting: answer.Result.Restarting, Program: answer.Result.Program, Version: answer.Result.Version,
		PreviousStartID: answer.Result.StartID, OperationID: answer.OperationID, Replayed: answer.Replayed,
	}
	if announce != nil {
		if err := announce(res); err != nil {
			return res, err
		}
	}
	if req.Wait <= 0 {
		return res, nil
	}
	sleep := req.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	started, err := waitForRestart(ctx, node, res.PreviousStartID, req.Wait, req.Home, sleep)
	if err != nil {
		return res, err
	}
	res.Running, res.StartID = true, started
	return res, nil
}

// waitForRestart reads status, now and then every restartPollInterval, until a
// daemon with a start identifier other than previous is running, and returns its
// identifier. The old daemon may answer for a moment after it has accepted the
// restart; then there is none while the program starts again, and the new daemon
// answers from its start on, before it runs. An old daemon, one that is going or
// gone, one that is starting and a read that was cut off are all "not yet"; any
// other failure ends the wait, because waiting does not mend it (a new daemon that
// this user may not talk to, for one). The time waited is counted as the intervals
// slept, as for a pairing.
func waitForRestart(ctx context.Context, node caller, previous string, wait time.Duration, home string,
	sleep func(context.Context, time.Duration) error) (string, error) {
	for waited := time.Duration(0); ; waited += restartPollInterval {
		view, err := readDaemon(ctx, node)
		switch {
		case err == nil && view.State == "running" && view.StartID != previous:
			return view.StartID, nil
		case err != nil && !notYet(err):
			return "", err
		case waited >= wait:
			return "", &failure{fmt.Sprintf("the daemon accepted the restart, but no new daemon was running within %s: it may still be starting, or the program could not be started. "+
				"Check the daemon's log (%s) and the output of whatever started the daemon; if the daemon is not running, start it the way it was started",
				wait, filepath.Join(home, "logs", "daemon.log"))}
		}
		if err := sleep(ctx, restartPollInterval); err != nil {
			return "", err
		}
	}
}

type daemonView struct{ State, StartID string }

func readDaemon(ctx context.Context, node caller) (daemonView, error) {
	raw, err := node.Call(ctx, "status", manageapi.Request{})
	if err != nil {
		return daemonView{}, err
	}
	var out struct {
		Daemon struct {
			State   string `json:"state"`
			StartID string `json:"start_id"`
		} `json:"daemon"`
	}
	if err := decodeResult(raw, &out); err != nil {
		return daemonView{}, err
	}
	return daemonView{out.Daemon.State, out.Daemon.StartID}, nil
}

// notYet says whether a failed read is what a daemon that is going away, gone or
// starting answers.
func notYet(err error) bool {
	e := apiwire.FromError(err)
	switch e.Code {
	case "daemon_not_running", "shutting_down", "recovering":
		return true
	}
	return e.Category == apiwire.CategoryOutcomeUnknown
}

// restartJSON is what `daemon restart --json` writes: the envelope of the other
// changes, with the result of the daemon that went away and what the wait saw.
type restartJSON struct {
	OperationID string `json:"operation_id"`
	Replayed    bool   `json:"replayed"`
	Result      struct {
		Restarting      bool   `json:"restarting"`
		Program         string `json:"program"`
		Version         string `json:"version"`
		PreviousStartID string `json:"previous_start_id"`
		Running         bool   `json:"running,omitempty"`
		StartID         string `json:"start_id,omitempty"`
	} `json:"result"`
}

// restartCommand is `daemon restart`: what is being started is printed as soon as the
// daemon has accepted the restart, so that it shows while the daemon restarts, then what the
// wait saw. With --json one object is written when the wait is over.
func (c *command) restartCommand(ctx context.Context, node caller, random io.Reader, req restartRequest) error {
	var announce func(restartResult) error
	if !c.json {
		announce = func(r restartResult) error {
			_, err := io.WriteString(c.out, renderRestarting(c.out, r))
			return err
		}
	}
	res, err := restartDaemon(ctx, node, random, req, announce)
	if err != nil {
		return err
	}
	if c.json {
		var out restartJSON
		out.OperationID, out.Replayed = res.OperationID, res.Replayed
		out.Result.Restarting, out.Result.Program, out.Result.Version = res.Restarting, res.Program, res.Version
		out.Result.PreviousStartID, out.Result.Running, out.Result.StartID = res.PreviousStartID, res.Running, res.StartID
		return c.encode(out)
	}
	_, err = io.WriteString(c.out, renderRestarted(c.out, res, res.Running))
	return err
}
