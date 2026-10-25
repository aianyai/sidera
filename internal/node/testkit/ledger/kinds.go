package ledger

import "slices"

// The kinds of entry. A kind says what happened and who writes it.
const (
	// Written by an adapter.
	KindAdapterStarted        = "adapter.started"
	KindAdapterLockAcquired   = "adapter.lock.acquired"
	KindAdapterLockRefused    = "adapter.lock.refused"
	KindSessionCreateReceived = "session.create.received"
	KindNativeCreateReturned  = "native.create.returned"
	KindPromptDeliverReceived = "prompt.deliver.received"
	KindPromptForwarded       = "prompt.forwarded"
	KindInterruptReceived     = "interrupt.received"
	KindCloseReceived         = "close.received"
	KindObservedSent          = "observed.sent"
	// Written by an adapter, a runtime or the fixture when a knob is used.
	KindKnobHit = "knob.hit"
	// Written by a runtime.
	KindRuntimeSessionCreated = "runtime.session.created"
	KindRuntimePromptReceived = "runtime.prompt.received"
	KindRuntimeTurnStarted    = "runtime.turn.started"
	KindRuntimeTurnEnded      = "runtime.turn.ended"
	KindRuntimeInterrupted    = "runtime.interrupted"
	KindRuntimeClosed         = "runtime.closed"
	KindRuntimeKilled         = "runtime.killed"
	// Written by a fake agent; model.saw also by a runtime for what its model read.
	KindModelSaw     = "model.saw"
	KindClientCall   = "client.call"
	KindClientResult = "client.result"
	// Written by the fixture.
	KindFixtureStep  = "fixture.step"
	KindFixtureKill  = "fixture.kill"
	KindFixtureCrash = "fixture.crash"
)

type kindSpec struct {
	required []string
	optional []string
}

func (s kindSpec) allows(name string) bool {
	return slices.Contains(s.required, name) || slices.Contains(s.optional, name)
}

// kindSpecs is the table of attributes by kind. Bodies of messages are never
// attributes: a digest stands for a body.
var kindSpecs = map[string]kindSpec{
	KindAdapterStarted:        {required: []string{"instance"}},
	KindAdapterLockAcquired:   {required: []string{"instance", "lock_path"}},
	KindAdapterLockRefused:    {required: []string{"instance", "lock_path"}},
	KindSessionCreateReceived: {required: []string{"attempt", "preset"}, optional: []string{"endpoint"}},
	KindNativeCreateReturned:  {required: []string{"attempt", "outcome"}, optional: []string{"native_ref", "runtime_writer", "runtime_seq"}},
	KindPromptDeliverReceived: {required: []string{"endpoint", "attempt", "text_digest"}},
	KindPromptForwarded:       {required: []string{"endpoint", "attempt", "text_digest"}},
	KindInterruptReceived:     {required: []string{"endpoint"}},
	KindCloseReceived:         {required: []string{"endpoint"}},
	KindObservedSent:          {required: []string{"lease", "mode", "verdict"}},
	KindKnobHit:               {required: []string{"knob"}, optional: []string{"detail"}},

	KindRuntimeSessionCreated: {required: []string{"attempt", "native_ref"}, optional: []string{"cwd", "pid"}},
	KindRuntimePromptReceived: {required: []string{"attempt", "text_digest"}},
	KindRuntimeTurnStarted:    {required: []string{"turn"}, optional: []string{"attempt"}},
	KindRuntimeTurnEnded:      {required: []string{"turn"}, optional: []string{"attempt"}},
	KindRuntimeInterrupted:    {},
	KindRuntimeClosed:         {},
	KindRuntimeKilled:         {},

	KindModelSaw: {
		required: []string{"endpoint", "message_id", "body_digest", "from"},
		optional: []string{"part", "parts", "possibly_duplicate", "feedback"},
	},
	KindClientCall:   {required: []string{"action"}, optional: []string{"endpoint", "idem_key"}},
	KindClientResult: {required: []string{"action", "category"}, optional: []string{"idem_key", "code", "message_id"}},

	KindFixtureStep:  {required: []string{"name"}},
	KindFixtureKill:  {required: []string{"pids"}},
	KindFixtureCrash: {required: []string{"crash_point"}, optional: []string{"pids"}},
}
