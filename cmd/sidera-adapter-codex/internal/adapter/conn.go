package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// dialTimeout bounds one attempt to reach the App Server, a start of it included.
const dialTimeout = 30 * time.Second

// errNotStarted is returned by calls that need the App Server before adapter.start
// said where it is.
var errNotStarted = errors.New("adapter: adapter.start has not been served")

// liveClient returns the connection when it is open, or nil.
func (a *App) liveClient() *appserver.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.liveClientLocked()
}

func (a *App) liveClientLocked() *appserver.Client {
	if a.client == nil {
		return nil
	}
	select {
	case <-a.client.Done():
		return nil
	default:
		return a.client
	}
}

// ensureClient returns the open connection, or opens one: the server is found or
// started, the connection initialized and the account read. The notifications and
// requests of the server are handled for as long as it stays open.
func (a *App) ensureClient(ctx context.Context) (*appserver.Client, error) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if c := a.liveClient(); c != nil {
		return c, nil
	}
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return nil, errNotStarted
	}
	ep, err := srv.Connect(ctx)
	if err != nil {
		return nil, err
	}
	c, err := appserver.Dial(ctx, ep, appserver.Handlers{ServerRequest: a.onServerRequest, Notification: a.onNotification})
	if err != nil {
		return nil, err
	}
	// The experimental API is asked for because the queue of a thread's prompts,
	// which delivers a prompt to a session a person runs, is part of it.
	init, err := c.Initialize(ctx, appserver.InitializeParams{
		ClientInfo:   appserver.ClientInfo{Name: "sidera-adapter-codex", Version: ClientVersion()},
		Capabilities: &appserver.InitializeCapabilities{OptOutNotificationMethods: appserver.QuietNotifications, ExperimentalAPI: true},
	})
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	signedIn := true
	if acct, err := c.AccountRead(ctx); err != nil {
		a.log.Warn("cannot read the account", "error", err)
	} else {
		signedIn = acct.SignedIn()
	}
	a.mu.Lock()
	a.client, a.serverVer, a.signedIn = c, appserver.ServerVersion(init.UserAgent), signedIn
	// A connection holds only the threads it started or resumed, and the server
	// unloads a thread when the last connection holding it ends: in a new
	// connection every thread is unloaded until it is resumed, with its
	// configuration, which the server keeps none of.
	for _, s := range a.sessions {
		s.status = appserver.ThreadNotLoaded
	}
	a.mu.Unlock()
	go a.watch(c)
	return c, nil
}

// watch waits for the connection to end and starts the effort to get it back.
func (a *App) watch(c *appserver.Client) {
	<-c.Done()
	a.mu.Lock()
	if a.client == c {
		a.client = nil
	}
	a.mu.Unlock()
	if a.shuttingDown() {
		return
	}
	a.log.Warn("the connection to the App Server ended", "error", c.Err())
	a.publishAvailability(false)
	a.startReconnect()
}

func (a *App) startReconnect() {
	a.mu.Lock()
	if a.reconnect || a.shutdownNow {
		a.mu.Unlock()
		return
	}
	a.reconnect = true
	a.mu.Unlock()
	go a.reconnectLoop()
}

// reconnectLoop reaches the server again, starting it when it is gone, and
// resumes every session in the new connection. The sessions' threads are loaded
// only for as long as a connection holds them, and their configuration is not
// kept by the server, so each is resumed with it.
func (a *App) reconnectLoop() {
	delay := a.opts.ReconnectMin
	for {
		select {
		case <-a.bgCtx.Done():
			return
		case <-time.After(delay):
		}
		ctx, cancel := context.WithTimeout(a.bgCtx, dialTimeout)
		c, err := a.ensureClient(ctx)
		cancel()
		if err != nil {
			a.log.Warn("the App Server is still unreachable", "error", err)
			delay = min(delay*2, a.opts.ReconnectMax)
			continue
		}
		failed := a.resync(c)
		a.publishAvailability(false)
		a.mu.Lock()
		if a.liveClientLocked() != nil && failed == 0 {
			a.reconnect = false
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()
		// The connection ended while the sessions were being resumed, or a session
		// could not be resumed for now: again, after a wait, for the sessions that
		// are still not loaded.
		if failed > 0 {
			delay = min(delay*2, a.opts.ReconnectMax)
		}
	}
}

// resync resumes every session whose thread is not loaded and reports the turns
// that began or ended while there was no connection. It returns how many sessions
// could not be resumed for now, which the reconnect loop tries again. A session
// whose thread the server does not have (it is lost) and one whose preset cannot
// be used are not retried: neither changes by waiting.
func (a *App) resync(c *appserver.Client) (failed int) {
	a.mu.Lock()
	sessions := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		// An attached session is never loaded here; its watcher reaches the server
		// again by itself.
		if !s.attached && !s.creating && s.status == appserver.ThreadNotLoaded {
			sessions = append(sessions, s)
		}
	}
	a.mu.Unlock()
	for _, s := range sessions {
		if _, ok := a.presetOf(s.preset); !ok {
			a.log.Warn("a session cannot be resumed: its preset is not usable", "endpoint", s.endpoint, "preset", s.preset)
			continue
		}
		ctx, cancel := context.WithTimeout(a.bgCtx, dialTimeout)
		state, _ := a.loadUnloaded(ctx, c, s, true)
		cancel()
		if state != adapterproto.AttachAttached {
			a.log.Warn("a session could not be resumed", "endpoint", s.endpoint, "state", state)
		}
		if state == adapterproto.AttachUnreachable {
			failed++
		}
	}
	return failed
}

// attach loads the session's thread in the connection and brings the adapter's
// books on its turns up to date. With report, a turn found running that was not
// known is reported as started: the daemon was told of every turn before the
// connection ended and thinks the session idle. Without it (the adapter has just
// started) the daemon learns the state from the answer to adapter.start.
//
// A caller that holds the session's loadMu does the same with attachLocked.
func (a *App) attach(ctx context.Context, c *appserver.Client, s *session, report bool) (adapterproto.AttachState, bool) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	return a.attachLocked(ctx, c, s, report)
}

// loadUnloaded is attach for a caller that found the thread not loaded: a thread
// another caller loaded while this one waited is not resumed again, the caller that
// loaded it having reported what it found.
func (a *App) loadUnloaded(ctx context.Context, c *appserver.Client, s *session, report bool) (adapterproto.AttachState, bool) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if state, active, ok := a.loadedBy(s); ok {
		return state, active
	}
	return a.attachLocked(ctx, c, s, report)
}

// loadedBy says whether the session's thread is loaded in the connection, with the
// state and activity attach answers for it. The caller holds the session's loadMu.
func (a *App) loadedBy(s *session) (state adapterproto.AttachState, active, loaded bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.status == appserver.ThreadNotLoaded {
		return "", false, false
	}
	return adapterproto.AttachAttached, s.busy(), true
}

// attachLocked is attach for a caller that holds the session's loadMu.
func (a *App) attachLocked(ctx context.Context, c *appserver.Client, s *session, report bool) (adapterproto.AttachState, bool) {
	preset, ok := a.presetOf(s.preset)
	if !ok {
		return adapterproto.AttachUnreachable, false
	}
	th, err := a.threadResume(ctx, c, s, s.threadID, preset)
	if err != nil {
		if appserver.IsThreadGone(err) {
			a.mu.Lock()
			s.lost = true
			a.mu.Unlock()
			return adapterproto.AttachLost, false
		}
		return adapterproto.AttachUnreachable, false
	}
	a.mu.Lock()
	s.lost, s.status = false, th.Status.Type
	a.mu.Unlock()
	a.catchUp(ctx, c, s, report)
	a.mu.Lock()
	defer a.mu.Unlock()
	return adapterproto.AttachAttached, s.busy()
}
