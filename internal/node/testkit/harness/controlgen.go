package harness

import (
	"fmt"
	"math/rand"
	"sort"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The paths of a control signal the generator draws, named by what makes each one
// end differently. A model test that judges what happens to every record asserts that
// each was taken (ControlGen.Paths), so a generator that stopped reaching one fails
// the test that relies on it.
const (
	// PathCreate asks the runtime endpoint for a session with a preset it has.
	PathCreate = "create"
	// PathCreateWithMessage is PathCreate with an initial message.
	PathCreateWithMessage = "create-with-message"
	// PathCreateRefused names a preset the node does not have: the node answers with a
	// reply that fails the creation, and a message that came with it was never taken.
	PathCreateRefused = "create-refused"
	// PathStatusDirect, PathStatusRuntime and PathStatusSession ask an endpoint of each
	// kind how it is, which the node answers itself.
	PathStatusDirect  = "status-direct"
	PathStatusRuntime = "status-runtime"
	PathStatusSession = "status-session"
	// PathInterruptSession and PathCloseSession act on a session the generator made and
	// has not closed.
	PathInterruptSession = "interrupt-session"
	PathCloseSession     = "close-session"
	// PathSignalClosedSession acts on a session the generator closed. Whether the close
	// was carried out when the signal reaches the node decides whether the node takes it
	// in or refuses it.
	PathSignalClosedSession = "signal-closed-session"
	// PathUnsupportedDirect asks a direct endpoint to close or to stop a turn, which it
	// does not take: the node answers that it is not supported.
	PathUnsupportedDirect = "unsupported-direct"
	// PathMalformedRuntime asks the runtime endpoint to close or to stop a turn, which
	// the rules of the node call malformed.
	PathMalformedRuntime = "malformed-runtime"
)

// ControlPaths lists every path, in a fixed order.
var ControlPaths = []string{
	PathCreate, PathCreateWithMessage, PathCreateRefused, PathStatusDirect, PathStatusRuntime, PathStatusSession,
	PathInterruptSession, PathCloseSession, PathSignalClosedSession, PathUnsupportedDirect, PathMalformedRuntime,
}

// ControlStep is one control signal the generator sent: which path it took, who sent it,
// to whom, and the message that carries it.
type ControlStep struct {
	Path    string
	Sender  string
	Target  string
	Signal  adapterproto.ControlSignal
	Message adapterproto.MessageID
	// Session is the session endpoint the signal acts on, zero for a signal that does
	// not act on one the generator made.
	Session ident.EndpointID
}

// ControlGen draws control signals for the model test of what becomes of every record:
// creations with and without an initial message, creations the node refuses, statuses of
// endpoints of every kind, and interrupts and closes of sessions it made, of one it
// closed, and of endpoints that do not take them. It knows which sessions it made from
// the replies to its creations, which come to the sender only after a pull, so the test
// moves the clock between steps as it moves it between any two operations.
//
// The draw is a function of the seed alone, and the paths a step can take depend only
// on what the generator has learned of its own sessions, so two runs of one seed with
// the same clock moves send the same signals.
type ControlGen struct {
	h       *H
	rng     *rand.Rand
	senders []string
	targets []string

	pending  map[adapterproto.MessageID]string // creations whose reply was not read, by sender
	sessions []ident.EndpointID                // sessions made and not closed by a step
	closed   []ident.EndpointID                // sessions a step closed
	paths    map[string]int
	sent     int
}

// NewControlGen returns a generator that sends as the agents of senders (names registered
// with H.Direct) to the direct endpoints named targets, the runtime endpoint and the
// sessions it makes. The node has to have an adapter with a runtime.
func NewControlGen(h *H, seed int64, senders, targets []string) *ControlGen {
	return &ControlGen{
		h: h, rng: rand.New(rand.NewSource(seed)), senders: senders, targets: targets,
		pending: map[adapterproto.MessageID]string{}, paths: map[string]int{},
	}
}

// Paths is how many times each path was taken.
func (g *ControlGen) Paths() map[string]int {
	out := make(map[string]int, len(g.paths))
	for k, v := range g.paths {
		out[k] = v
	}
	return out
}

// Sent is how many signals the generator sent.
func (g *ControlGen) Sent() int { return g.sent }

// learn reads the replies to the creations that had none and takes the sessions they
// made.
func (g *ControlGen) learn() error {
	ids := make([]adapterproto.MessageID, 0, len(g.pending))
	for id := range g.pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		st, err := g.h.Agent(g.pending[id]).Status(g.h.Ctx(), fakeagent.StatusInput{MessageID: id})
		if err != nil {
			return fmt.Errorf("harness: the status of the creation %s: %w", id, err)
		}
		if st.ControlReply == nil || st.ControlReply.Create == nil {
			continue
		}
		delete(g.pending, id)
		if c := st.ControlReply.Create; c.Outcome == adapterproto.CreateReplyCreated {
			ep, err := ident.ParseEndpointID(string(c.SessionEndpoint))
			if err != nil {
				return fmt.Errorf("harness: the session of the creation %s: %w", id, err)
			}
			g.sessions = append(g.sessions, ep)
		}
	}
	return nil
}

// Next sends one control signal and returns what it was. A refusal of the call itself
// (the queue of the node is full, a sender that is not there) is returned as the error:
// the paths the generator draws are all taken by the node as signals.
func (g *ControlGen) Next() (ControlStep, error) {
	if err := g.learn(); err != nil {
		return ControlStep{}, err
	}
	sender := g.senders[g.rng.Intn(len(g.senders))]
	step := ControlStep{Sender: sender}
	in := fakeagent.ControlInput{}
	pick := func(ids []ident.EndpointID) ident.EndpointID { return ids[g.rng.Intn(len(ids))] }

	// The paths that can be taken now, each with the weight of one, in a fixed order.
	options := []string{PathCreate, PathCreateWithMessage, PathCreateRefused, PathStatusRuntime, PathUnsupportedDirect, PathMalformedRuntime}
	if len(g.targets) > 0 {
		options = append(options, PathStatusDirect)
	}
	if len(g.sessions) > 0 {
		options = append(options, PathStatusSession, PathInterruptSession, PathCloseSession, PathInterruptSession, PathCloseSession)
	}
	if len(g.closed) > 0 {
		options = append(options, PathSignalClosedSession)
	}
	step.Path = options[g.rng.Intn(len(options))]
	step.Signal = adapterproto.SignalSessionCreate
	switch step.Path {
	case PathCreate:
		step.Target, in.Preset = g.h.RuntimeAddr(), "default"
	case PathCreateWithMessage:
		step.Target, in.Preset, in.Body = g.h.RuntimeAddr(), "default", fmt.Sprintf("initial message %d", g.sent)
	case PathCreateRefused:
		step.Target, in.Preset = g.h.RuntimeAddr(), "no-such-preset"
		if g.rng.Intn(2) == 0 {
			in.Body = fmt.Sprintf("initial message %d", g.sent)
		}
	case PathStatusDirect:
		step.Signal, step.Target = adapterproto.SignalEndpointStatus, g.h.Addr(g.targets[g.rng.Intn(len(g.targets))])
	case PathStatusRuntime:
		step.Signal, step.Target = adapterproto.SignalEndpointStatus, g.h.RuntimeAddr()
	case PathStatusSession:
		step.Session = pick(g.sessions)
		step.Signal, step.Target = adapterproto.SignalEndpointStatus, address(step.Session)
	case PathInterruptSession:
		step.Session = pick(g.sessions)
		step.Signal, step.Target = adapterproto.SignalSessionInterrupt, address(step.Session)
	case PathCloseSession:
		step.Session = pick(g.sessions)
		step.Signal, step.Target = adapterproto.SignalSessionClose, address(step.Session)
	case PathSignalClosedSession:
		step.Session = pick(g.closed)
		step.Signal, step.Target = g.either(), address(step.Session)
	case PathUnsupportedDirect:
		to := g.senders[g.rng.Intn(len(g.senders))]
		step.Signal, step.Target = g.either(), g.h.Addr(to)
	case PathMalformedRuntime:
		step.Signal, step.Target = g.either(), g.h.RuntimeAddr()
	}
	in.To, in.Signal = step.Target, step.Signal
	res, err := g.h.Agent(sender).Control(g.h.Ctx(), in)
	if err != nil {
		return step, fmt.Errorf("harness: %s by %s (%s): %w", step.Signal, sender, step.Path, err)
	}
	step.Message = res.MessageID
	g.paths[step.Path]++
	g.sent++
	switch step.Path {
	case PathCreate, PathCreateWithMessage, PathCreateRefused:
		g.pending[step.Message] = sender
	case PathCloseSession:
		g.sessions = remove(g.sessions, step.Session)
		g.closed = append(g.closed, step.Session)
	}
	return step, nil
}

// either is the close or the interrupt, at random.
func (g *ControlGen) either() adapterproto.ControlSignal {
	if g.rng.Intn(2) == 0 {
		return adapterproto.SignalSessionClose
	}
	return adapterproto.SignalSessionInterrupt
}

func address(id ident.EndpointID) string { return ident.Address{Node: NodeID(), Endpoint: id}.String() }

func remove(ids []ident.EndpointID, id ident.EndpointID) []ident.EndpointID {
	out := ids[:0:0]
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}
