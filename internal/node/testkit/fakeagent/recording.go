package fakeagent

import (
	"context"

	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// RecordingOptions says what Recording writes beyond the two entries of every call.
type RecordingOptions struct {
	// ModelSaw makes a Recv also write what the page showed, one model.saw entry
	// per message, for an agent that is not the model of a runtime's turn. A turn's
	// own receives are recorded by the runtime, so leave it off for an agent that
	// a runtime drives: two writers would count each message twice.
	ModelSaw bool
	// Feedback classifies a message of a page as a notice of the node itself.
	Feedback func(from, body string) (kind string, ok bool)
}

// Recording wraps an agent so that every call writes two entries to w: client.call
// before the action starts, so that an attempt a kill interrupted is still on
// record, and client.result after it, with the category and code of the error or
// "ok" and the message it made. A test judges that a message was sent from the
// result entry and never from the daemon's own state. Bodies are never written.
func Recording(inner Agent, w *ledger.Writer, endpoint string, opts RecordingOptions) Agent {
	return &recording{inner: inner, w: w, endpoint: endpoint, opts: opts}
}

type recording struct {
	inner    Agent
	w        *ledger.Writer
	endpoint string
	opts     RecordingOptions
}

// run writes the call, runs the action and writes its result. A failure to write
// the ledger is a failure of the test, so it panics: continuing would leave an
// oracle with holes.
func (r *recording) run(action, idem string, do func() (messageID string, err error)) {
	must(r.w.Append(ledger.KindClientCall, ledger.Attrs{Action: action, Endpoint: r.endpoint, IdemKey: idem}))
	id, err := do()
	res := ledger.Attrs{Action: action, IdemKey: idem, MessageID: id, Category: "ok"}
	if err != nil {
		res.Category, res.Code = string(CategoryOf(err)), CodeOf(err)
	}
	must(r.w.Append(ledger.KindClientResult, res))
}

func must(err error) {
	if err != nil {
		panic("fakeagent: " + err.Error())
	}
}

func (r *recording) Me(ctx context.Context) (res MeResult, err error) {
	r.run("me", "", func() (string, error) {
		res, err = r.inner.Me(ctx)
		return "", err
	})
	return res, err
}

func (r *recording) Peers(ctx context.Context) (res PeersResult, err error) {
	r.run("peers", "", func() (string, error) {
		res, err = r.inner.Peers(ctx)
		return "", err
	})
	return res, err
}

func (r *recording) Directory(ctx context.Context, in DirectoryInput) (res DirectoryResult, err error) {
	r.run("directory", "", func() (string, error) {
		res, err = r.inner.Directory(ctx, in)
		return "", err
	})
	return res, err
}

func (r *recording) Probe(ctx context.Context, in ProbeInput) (res ProbeResult, err error) {
	r.run("probe", "", func() (string, error) {
		res, err = r.inner.Probe(ctx, in)
		return "", err
	})
	return res, err
}

func (r *recording) Send(ctx context.Context, in SendInput) (res SendResult, err error) {
	r.run("send", in.IdemKey, func() (string, error) {
		res, err = r.inner.Send(ctx, in)
		return string(res.MessageID), err
	})
	return res, err
}

func (r *recording) Reply(ctx context.Context, in ReplyInput) (res SendResult, err error) {
	r.run("reply", in.IdemKey, func() (string, error) {
		res, err = r.inner.Reply(ctx, in)
		return string(res.MessageID), err
	})
	return res, err
}

func (r *recording) Recv(ctx context.Context, in RecvInput) (res RecvResult, err error) {
	r.run("recv", "", func() (string, error) {
		res, err = r.inner.Recv(ctx, in)
		return "", err
	})
	if err == nil && r.opts.ModelSaw {
		for _, attrs := range fakeruntime.ModelSaw(r.endpoint, res.Page, r.opts.Feedback) {
			must(r.w.Append(ledger.KindModelSaw, attrs))
		}
	}
	return res, err
}

func (r *recording) Ack(ctx context.Context, in AckInput) (res AckResult, err error) {
	r.run("ack", "", func() (string, error) {
		res, err = r.inner.Ack(ctx, in)
		return "", err
	})
	return res, err
}

func (r *recording) Control(ctx context.Context, in ControlInput) (res SendResult, err error) {
	r.run("control", in.IdemKey, func() (string, error) {
		res, err = r.inner.Control(ctx, in)
		return string(res.MessageID), err
	})
	return res, err
}

func (r *recording) Declare(ctx context.Context, in DeclareInput) (err error) {
	r.run("declare", "", func() (string, error) {
		err = r.inner.Declare(ctx, in)
		return "", err
	})
	return err
}

func (r *recording) Status(ctx context.Context, in StatusInput) (res StatusResult, err error) {
	r.run("status", "", func() (string, error) {
		res, err = r.inner.Status(ctx, in)
		return "", err
	})
	return res, err
}
