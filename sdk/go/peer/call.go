package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// Call sends a request and waits for the answer, decoding the result into
// result (which may be nil to discard it, or a *json.RawMessage to keep it
// raw). params must marshal to a JSON object or array, or be nil.
//
// The error says what is known about the request's effect; see NoEffect and
// ResultUnknown. A *Error is the other side's error response. When ctx is done
// before the answer arrives, Call tells the other side to stop with
// $/cancel_request, returns ctx.Err() and treats the outcome as unknown; an
// answer that arrives later is dropped.
func (p *Peer) Call(ctx context.Context, method string, params, result any) error {
	line, id, err := p.encodeCall(ctx, method, params)
	if err != nil {
		return err
	}
	c := &pendingCall{done: make(chan callResult, 1)}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrClosed
	}
	select {
	case p.out <- outItem{line: line}:
		p.pending[id] = c
		p.mu.Unlock()
	default:
		p.mu.Unlock()
		return ErrOverloaded
	}

	select {
	case res := <-c.done:
		return decodeResult(res, result)
	case <-ctx.Done():
		p.mu.Lock()
		_, waiting := p.pending[id]
		delete(p.pending, id)
		p.mu.Unlock()
		if !waiting {
			// The answer or the close got in first; it is already on its way.
			return decodeResult(<-c.done, result)
		}
		p.sendCancel(id)
		return ctx.Err()
	}
}

func (p *Peer) encodeCall(ctx context.Context, method string, params any) (line []byte, id int64, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	if method == "" {
		return nil, 0, fmt.Errorf("%w: empty method", ErrNotSent)
	}
	raw, err := marshalParams(params)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	id = p.nextID.Add(1)
	line, err = requestLine(id, method, raw)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	if len(line)-1 > p.params.MaxLineBytes {
		return nil, 0, ErrMessageTooLarge
	}
	return line, id, nil
}

func decodeResult(res callResult, into any) error {
	if res.err != nil {
		return res.err
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(res.result, into); err != nil {
		return fmt.Errorf("peer: decode result: %w", err)
	}
	return nil
}

// sendCancel asks the other side to stop working on request id. It is
// advisory, so it is dropped rather than waiting when the queue is full.
func (p *Peer) sendCancel(id int64) {
	params := json.RawMessage(`{"request_id":` + strconv.FormatInt(id, 10) + `}`)
	line, err := notificationLine(MethodCancelRequest, params)
	if err != nil {
		return
	}
	select {
	case p.out <- outItem{line: line}:
	default:
		p.log.Warn("cancel request not sent: outbound queue full")
	}
}

// Notify sends a notification. It returns once the line is queued, not once it
// is written. ErrOverloaded means the outbound queue was full and nothing was
// queued.
func (p *Peer) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	if method == "" {
		return fmt.Errorf("%w: empty method", ErrNotSent)
	}
	raw, err := marshalParams(params)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	line, err := notificationLine(method, raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	if len(line)-1 > p.params.MaxLineBytes {
		return ErrMessageTooLarge
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	select {
	case p.out <- outItem{line: line}:
		return nil
	default:
		return ErrOverloaded
	}
}
