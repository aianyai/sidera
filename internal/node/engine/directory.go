package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"slices"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/publication"
)

// DefaultDirectoryLimit keeps ordinary listings short while allowing callers
// to request the node's whole endpoint cap in one page.
const DefaultDirectoryLimit = 100

// DirectoryQuery names the node and the live page to read. A zero Node means
// this node, and a zero Limit takes the default page size.
type DirectoryQuery struct {
	Node   ident.NodeID
	Cursor string
	Limit  int
}

// DirectoryPage is the page authorized by its target node.
type DirectoryPage = transport.DirectoryPage

// directoryTask has one answer and one lifetime. The outbox mutex serializes
// answering, abandoning and queuing it, so an abandoned task is never answered
// or queued again. The transport runs with its context, independently of the
// context that owns the worker and the other work ahead of it.
type directoryTask struct {
	ctx      context.Context
	cancel   context.CancelFunc
	answer   call[DirectoryPage]
	finished bool
}

func (e *Engine) finishDirectory(t *directoryTask, page DirectoryPage, err error) {
	e.outbox.mu.Lock()
	defer e.outbox.mu.Unlock()
	if t.finished || t.ctx.Err() != nil {
		return
	}
	t.finished = true
	t.answer.done(page, err)
}

func (e *Engine) abandonDirectory(t *directoryTask) {
	e.outbox.mu.Lock()
	defer e.outbox.mu.Unlock()
	t.finished = true
	t.cancel()
	e.outbox.queue = slices.DeleteFunc(e.outbox.queue, func(w outWork) bool { return w.directory == t })
}

type cmdDirectory struct {
	e      *Engine
	task   *directoryTask
	caller Caller
	query  DirectoryQuery
}

func (c *cmdDirectory) gate() gate     { return gateRead }
func (c *cmdDirectory) fail(err error) { c.e.finishDirectory(c.task, DirectoryPage{}, err) }
func (c *cmdDirectory) run(e *Engine, _ time.Time) {
	if c.task.ctx.Err() != nil {
		return
	}
	st, err := e.view.resolve(c.caller)
	if err == nil {
		err = e.directoryState()
	}
	req, requestErr := normalizeDirectoryRequest(transport.DirectoryRequest{Cursor: c.query.Cursor, Limit: c.query.Limit})
	if err == nil {
		err = requestErr
	}
	if err == nil && e.tr == nil {
		err = refuse(ReasonDirectoryUnavailable)
	}
	if err != nil {
		c.fail(err)
		return
	}
	node := c.query.Node
	if node == (ident.NodeID{}) {
		node = e.d.Node
	}
	e.give(outWork{kind: workDirectory, node: node, sender: st.rec.ID, directory: c.task,
		directoryRequest: req})
}

// Directory reads through the outbound FIFO, using only the caller resolved by
// its source command. Its independent timer also bounds a queue held by a call
// ahead of it or a loop whose time-driven work is paused.
func (e *Engine) Directory(ctx context.Context, caller Caller, query DirectoryQuery) (DirectoryPage, error) {
	if err := ctx.Err(); err != nil {
		return DirectoryPage{}, err
	}
	taskCtx, cancel := context.WithCancel(ctx)
	t := &directoryTask{ctx: taskCtx, cancel: cancel, answer: newCall[DirectoryPage]()}
	defer e.abandonDirectory(t)
	c := &cmdDirectory{e: e, task: t, caller: caller, query: query}
	if err := e.enqueue(ctx, submitExternal, c); err != nil {
		return DirectoryPage{}, err
	}
	timer := e.d.Clock.NewTimer(e.p.DirectoryWait)
	defer timer.Stop()
	select {
	case result := <-t.answer.ch:
		return result.v, result.err
	case <-timer.C():
		return DirectoryPage{}, refuse(ReasonDirectoryUnavailable)
	case <-e.shutdown:
		return DirectoryPage{}, refuse(ReasonShuttingDown)
	case <-e.done:
		return DirectoryPage{}, refuse(ReasonShuttingDown)
	case <-e.ctx.Done():
		return DirectoryPage{}, refuse(ReasonShuttingDown)
	case <-ctx.Done():
		return DirectoryPage{}, ctx.Err()
	}
}

func (e *Engine) directoryState() error {
	switch {
	case e.Phase() >= PhaseStopping:
		return refuse(ReasonShuttingDown)
	case e.Phase() == PhaseRecovering:
		return refuse(ReasonRecovering)
	case e.paused:
		return refuse(ReasonClockRollback)
	}
	return nil
}

type cmdDirectoryResult struct {
	call[struct{}]
	task *directoryTask
	page DirectoryPage
	err  error
}

func (c *cmdDirectoryResult) gate() gate { return gateLoopback }
func (c *cmdDirectoryResult) run(e *Engine, _ time.Time) {
	err := e.directoryState()
	if err == nil {
		err = c.err
		if errors.Is(err, transport.ErrUnreachable) || errors.Is(err, transport.ErrTimeout) {
			err = refuse(ReasonDirectoryUnavailable)
		}
	}
	e.finishDirectory(c.task, c.page, err)
	c.done(struct{}{}, nil)
}

type cmdQueryDirectory struct {
	call[DirectoryPage]
	from   transport.Origin
	sender ident.EndpointID
	req    transport.DirectoryRequest
}

func (c *cmdQueryDirectory) gate() gate { return gateLoopback }
func (c *cmdQueryDirectory) run(e *Engine, now time.Time) {
	if err := e.directoryState(); err != nil {
		c.fail(err)
		return
	}
	req, err := normalizeDirectoryRequest(c.req)
	if err != nil {
		c.fail(err)
		return
	}
	if e.directoryKey == nil {
		key := make([]byte, sha256.Size)
		if _, err := io.ReadFull(e.randomReader(), key); err != nil {
			c.fail(e.classifyPeerCall("directory", c.from, err))
			return
		}
		e.directoryKey = key
	}
	asker := ident.Address{Node: c.from.Node(), Endpoint: c.sender}
	after, err := e.readDirectoryCursor(asker, req.Cursor)
	if err != nil {
		c.fail(err)
		return
	}
	page := DirectoryPage{Endpoints: make([]transport.DirectoryEntry, 0)}
	for id, st := range e.view.endpoints {
		if st.rec.Status != store.EndpointOpen || (!after.IsZero() && id.String() <= after.String()) {
			continue
		}
		v := e.evaluate(now, authCheck{
			stage: authz.StageIntake, local: c.from.Local(), sender: authz.Sender{Node: c.from.Node(), Endpoint: c.sender},
			action: authz.ActionSee, target: authz.Target{Kind: st.rec.Kind.TargetKind(), Endpoint: id}, decl: st.decl,
		})
		if v.Outcome != authz.OutcomeAllow {
			continue
		}
		signals := adapterreg.KindSignals(st.rec.Kind)
		if adm, ok := e.admission(st.rec.Instance); ok {
			signals = adm.Signals(st.rec.Kind)
		}
		page.Endpoints = append(page.Endpoints, transport.DirectoryEntry{
			Address: ident.Address{Node: e.d.Node, Endpoint: id}, Kind: st.rec.Kind,
			DisplayName: st.rec.DisplayName, Description: st.rec.Description, Accepts: signals,
		})
	}
	sort.Slice(page.Endpoints, func(i, j int) bool {
		return page.Endpoints[i].Address.Endpoint.String() < page.Endpoints[j].Address.Endpoint.String()
	})
	if len(page.Endpoints) > req.Limit {
		page.Endpoints = page.Endpoints[:req.Limit]
		page.NextCursor = e.makeDirectoryCursor(asker, page.Endpoints[req.Limit-1].Address.Endpoint)
	}
	c.done(page, nil)
}

func normalizeDirectoryRequest(req transport.DirectoryRequest) (transport.DirectoryRequest, error) {
	if req.Limit == 0 {
		req.Limit = DefaultDirectoryLimit
	}
	if req.Limit < 1 || req.Limit > publication.MaxEndpointsPerNode {
		return req, invalidArgument("limit", errors.New("directory limit is outside the node's endpoint cap"))
	}
	return req, nil
}

// A cursor's payload is version, asking node, asking endpoint and last endpoint.
// The process-only HMAC binds both the caller and the lifetime without retaining
// pages. It is a position, never evidence of permission: every page is judged anew.
const directoryCursorPayloadLen = 1 + ident.NodeIDLen + 2*ident.EndpointIDLen

func (e *Engine) makeDirectoryCursor(asker ident.Address, last ident.EndpointID) string {
	payload := []byte{1}
	payload = append(payload, asker.Node.Bytes()...)
	payload = append(payload, asker.Endpoint.Bytes()...)
	payload = append(payload, last.Bytes()...)
	mac := hmac.New(sha256.New, e.directoryKey)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(payload))
}

func (e *Engine) readDirectoryCursor(asker ident.Address, raw string) (ident.EndpointID, error) {
	if raw == "" {
		return ident.EndpointID{}, nil
	}
	invalid := &Refusal{Reason: ReasonInvalidArgument, Field: "cursor", Detail: DetailCursorInvalid}
	if len(raw) != base64.RawURLEncoding.EncodedLen(directoryCursorPayloadLen+sha256.Size) {
		return ident.EndpointID{}, invalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(data) != directoryCursorPayloadLen+sha256.Size || base64.RawURLEncoding.EncodeToString(data) != raw {
		return ident.EndpointID{}, invalid
	}
	payload := data[:directoryCursorPayloadLen]
	mac := hmac.New(sha256.New, e.directoryKey)
	mac.Write(payload)
	if payload[0] != 1 || !hmac.Equal(mac.Sum(nil), data[directoryCursorPayloadLen:]) ||
		!bytes.Equal(payload[1:1+ident.NodeIDLen], asker.Node.Bytes()) ||
		!bytes.Equal(payload[1+ident.NodeIDLen:1+ident.NodeIDLen+ident.EndpointIDLen], asker.Endpoint.Bytes()) {
		return ident.EndpointID{}, invalid
	}
	last, err := ident.EndpointIDFromBytes(payload[1+ident.NodeIDLen+ident.EndpointIDLen:])
	if err != nil {
		return ident.EndpointID{}, invalid
	}
	return last, nil
}

// QueryDirectory is the target side of a directory read. Only the transport can
// attest a local origin; visibility is judged by the same evaluator as intake.
func (e *Engine) QueryDirectory(ctx context.Context, from transport.Origin, sender ident.EndpointID, req transport.DirectoryRequest) (DirectoryPage, error) {
	c := &cmdQueryDirectory{call: newCall[DirectoryPage](), from: from, sender: sender, req: req}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}
