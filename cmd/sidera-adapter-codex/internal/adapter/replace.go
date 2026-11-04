package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
)

// replaceTimeout bounds the ending of the App Server in a replacement.
const replaceTimeout = 30 * time.Second

// upgradeState is what the adapter knows about replacing the App Server after
// Codex was upgraded under it.
type upgradeState struct {
	// pending is set while the installed Codex is not the version the server runs
	// and the server will be replaced once it is idle.
	pending bool
	// tried is the pair of versions (see versionPair) for which the server was
	// replaced, or could not be shown to be one the adapter started. The same pair
	// is not tried again: a replacement that leaves the versions apart (a wrapper
	// around Codex that reports another version than the server does) would
	// otherwise end the server at every check.
	tried string
}

func versionPair(installed, serving string) string { return installed + " -> " + serving }

// newServer makes the manager of the App Server for the Codex program as it is
// now. The caller holds a.mu.
func (a *App) newServer() ServerManager {
	return a.opts.NewServer(server.Config{
		Program: a.codexPath, StateDir: a.inst.StateDir, Environ: a.opts.Environ, Logger: a.log,
	})
}

// enterServerOp is called by an operation that cannot have the App Server replaced
// while it runs: a turn start, a session creation, a prompt queued for an attached
// session, a command run through the server. leave ends it. ok is false while the
// server is being replaced; the operation then answers as it does when there is no
// connection.
func (a *App) enterServerOp() (leave func(), ok bool) {
	if !a.replaceGate.TryRLock() {
		return nil, false
	}
	return a.replaceGate.RUnlock, true
}

// watchCodexVersion compares the installed Codex with the server's version every
// UpgradeCheckEvery for as long as the adapter runs.
func (a *App) watchCodexVersion() {
	tick := time.NewTicker(a.opts.UpgradeCheckEvery)
	defer tick.Stop()
	for {
		select {
		case <-a.bgCtx.Done():
			return
		case <-tick.C:
			a.checkServer(a.bgCtx)
		}
	}
}

// turnEnded is called when a turn has ended: a replacement that waited for the
// sessions to be idle may be possible now.
func (a *App) turnEnded() {
	a.mu.Lock()
	pending := a.upgrade.pending
	a.mu.Unlock()
	if pending {
		go a.checkServer(a.bgCtx)
	}
}

// checkServer replaces the App Server when the Codex now installed is not the
// version it runs and nothing is using it. The program is resolved again, because
// an upgrade may have moved it. Calls are made at the start of the adapter, every
// UpgradeCheckEvery and when a turn ends while a replacement waits; one that finds
// another running returns at once.
//
// The server is ended inside the replacement gate and only when every session is
// idle (see busyReason). The connection then closes, the reconnect loop starts
// the server afresh from the installed program and resumes the sessions in it,
// which is the path a server that died takes; the threads are in Codex's own
// store and survive the server.
func (a *App) checkServer(ctx context.Context) {
	if !a.checkMu.TryLock() {
		return
	}
	defer a.checkMu.Unlock()

	a.mu.Lock()
	client, serving, program := a.liveClientLocked(), a.serverVer, a.instance.CodexProgram
	a.mu.Unlock()
	if client == nil || serving == "" {
		return // no server is known to be running
	}
	path, err := a.opts.LookPath(program)
	if err != nil {
		a.log.Debug("cannot compare the server with Codex: the program was not found", "error", err)
		return
	}
	installed, err := a.opts.CodexVersion(ctx, path)
	if err != nil {
		a.log.Debug("cannot compare the server with Codex: its version cannot be read", "error", err)
		return
	}

	pair := versionPair(installed, serving)
	a.mu.Lock()
	a.codexPath, a.codexVer = path, installed
	switch {
	case installed == serving:
		a.upgrade = upgradeState{}
	case a.upgrade.tried == pair:
		a.upgrade.pending = false
	default:
		a.upgrade.pending = true
	}
	pending := a.upgrade.pending
	a.mu.Unlock()
	if !pending {
		return
	}

	if !a.replaceGate.TryLock() {
		a.log.Debug("the App Server will be replaced when it is idle: an operation is using it", "serving", serving, "installed", installed)
		return
	}
	defer a.replaceGate.Unlock()
	if why := a.busyReason(); why != "" {
		a.log.Debug("the App Server will be replaced when it is idle", "serving", serving, "installed", installed, "busy", why)
		return
	}
	a.replace(ctx, client, serving, installed, pair)
}

// replace ends the idle App Server. The caller holds the gate exclusively and has
// seen every session idle.
func (a *App) replace(ctx context.Context, client *appserver.Client, serving, installed, pair string) {
	a.log.Info("replacing the App Server: Codex was upgraded and no session is running", "serving", serving, "installed", installed)
	a.mu.Lock()
	srv := a.newServer() // for the program as it is now
	a.srv = srv
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, replaceTimeout)
	defer cancel()
	err := srv.Terminate(ctx)
	switch {
	case errors.Is(err, server.ErrNotProven):
		a.mu.Lock()
		a.upgrade = upgradeState{tried: pair}
		a.mu.Unlock()
		a.log.Warn("the App Server was left running: it cannot be shown to be one this adapter started, restart it by hand", "serving", serving, "installed", installed, "error", err)
		return
	case err != nil:
		a.log.Warn("cannot end the App Server to replace it; trying again later", "error", err)
		return
	}
	a.mu.Lock()
	a.upgrade = upgradeState{tried: pair}
	a.mu.Unlock()
	// The server is gone; the connection is closed so that nothing is sent over it
	// and the reconnect loop starts at once.
	_ = client.Close()
}

// busyReason says why the App Server cannot be ended now, or is empty when every
// session is idle. Idle is strict: a session counts as busy unless its thread is
// known to be loaded in this connection and idle, so that a state the adapter has
// not seen or does not know never lets a turn be cut short (the caller has found
// the connection open). Sessions of a thread a
// person runs (attached) are not the server's: their turns run in that person's own
// Codex. The caller holds the gate exclusively, so no operation is in flight.
func (a *App) busyReason() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.sessions {
		switch {
		case s.attached || s.lost:
			// An attached session has no turn in this server; a lost thread has none
			// anywhere.
		case s.creating:
			return fmt.Sprintf("session %s is being created", s.endpoint)
		case s.pending != nil:
			return fmt.Sprintf("a turn of session %s is being started", s.endpoint)
		case s.activeTurn != "":
			return fmt.Sprintf("session %s runs a turn", s.endpoint)
		case s.status != appserver.ThreadIdle:
			return fmt.Sprintf("the thread of session %s is %q", s.endpoint, s.status)
		}
	}
	return ""
}
