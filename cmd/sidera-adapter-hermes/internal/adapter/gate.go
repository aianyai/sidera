package adapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// gate asks whether the Hermes command line can be used right now, and tells the
// daemon when the answer changes. Both loops ask before they touch anything: a
// message is taken from Sidera only when the card it becomes can be written, since
// a message that is taken and not acknowledged is handed out again after its
// lease and each hand-out brings it nearer to being declared of unknown outcome.
//
// "Can be used" means the whole probe of the client: a Hermes release the adapter
// was built against, no profile the dispatcher would confuse with the lane, the
// board in place. The probe costs three Hermes processes, so it runs when the gate
// has never passed it, when it last failed, and every timing.verifyEvery: a Hermes
// that is upgraded under a running adapter, or a profile created later, is found
// within that time. Between probes the check is the one cheap command that needs
// the board.
//
// The same verification reads whether the gateways that wake the conversations run
// the Kanban notifier, and whether the agent of the home conversation has the toolsets
// it delegates with. There is one verification at a time: the three loops ask the
// gate together, and a caller that arrives while one runs waits for it and takes its
// result, so that Hermes is not asked by several processes at once and a slow, older
// answer never replaces a newer one. A notifier that is off, or a toolset that is
// missing, does not make Hermes unusable: cards are still read and written, so
// receiving goes on, but no conversation is woken by what is written, or the agent
// cannot delegate, and the daemon is told so in the same availability report.
type gate struct {
	board        *kanban.Client
	wakeProfiles []string
	report       func(context.Context, adapterproto.AvailabilityParams) error
	log          *slog.Logger
	now          func() time.Time
	verifyEvery  time.Duration

	// homeProfile and homePlatform are the conversation the adapter wakes by default:
	// the agent there is the one that delegates over this lane.
	homeProfile, homePlatform string

	// mu is held from the decision that the state changed until the daemon has been
	// told, so that the reports reach the daemon in the order of the changes.
	mu         sync.Mutex
	down       bool
	verifiedAt time.Time
	// notifierOff is the detail of the condition while a notifier is off, else empty.
	// It is what the last verification found; one that could not read it leaves it.
	notifierOff string
	// toolsetsMissing is the same for the toolsets of the home conversation's agent.
	toolsetsMissing string
	// reported is the state the daemon was last told.
	reported availability

	// flightMu guards verifying, the verification in progress.
	flightMu  sync.Mutex
	verifying *verification
}

// verification is a full verification in progress, which every caller that arrives
// meanwhile waits for.
type verification struct {
	done     chan struct{}
	err      error
	notifier notifierReading
	toolsets toolsetsReading
}

// availability is what the daemon is told: that Hermes is unusable, or else which
// notifier and toolset details stand.
type availability struct {
	down     bool
	notifier string
	toolsets string
}

// reading is what a verification found out beyond the probe.
type reading struct {
	notifier notifierReading
	toolsets toolsetsReading
}

// Check asks whether Hermes can be used, and reports a change. A failure caused by
// the end of ctx is not Hermes' and is returned without being reported.
func (g *gate) Check(ctx context.Context) error {
	full := g.verificationDue()
	var err error
	var found reading
	if full {
		found, err = g.verify(ctx)
	} else {
		var ok bool
		ok, err = g.board.BoardExists(ctx)
		if err == nil && !ok {
			err = errors.New("the board does not exist")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	g.set(ctx, err, full, found)
	return err
}

// verify runs the whole probe and, when Hermes is usable, reads the notifiers and the
// toolsets. A call that arrives while another runs waits for it and returns its result.
func (g *gate) verify(ctx context.Context) (reading, error) {
	g.flightMu.Lock()
	if v := g.verifying; v != nil {
		g.flightMu.Unlock()
		select {
		case <-v.done:
			return reading{v.notifier, v.toolsets}, v.err
		case <-ctx.Done():
			return reading{}, ctx.Err()
		}
	}
	v := &verification{done: make(chan struct{})}
	g.verifying = v
	g.flightMu.Unlock()

	v.err = g.board.Probe(ctx)
	if v.err == nil {
		v.notifier = g.readNotifier(ctx)
		v.toolsets = g.readToolsets(ctx)
	}
	g.flightMu.Lock()
	g.verifying = nil
	g.flightMu.Unlock()
	close(v.done)
	return reading{v.notifier, v.toolsets}, v.err
}

// notifierReading is what a verification found about the notifiers; read is false
// when it did not look or could not tell.
type notifierReading struct {
	read   bool
	detail string // empty when every notifier is on
}

// readNotifier reads the notifier setting of the profiles. A profile that cannot be
// read is logged and leaves the answer to the others: the profiles known to be off are
// reported even then, and when none is known off the reading says nothing, so that the
// last answer stands.
func (g *gate) readNotifier(ctx context.Context) notifierReading {
	off, err := g.board.NotifierDisabled(ctx, g.wakeProfiles)
	if err != nil {
		if ctx.Err() == nil {
			g.log.Warn("could not read whether the Hermes gateway's Kanban notifier is on", "error", err)
		}
		if len(off) == 0 {
			return notifierReading{}
		}
	}
	return notifierReading{read: true, detail: notifierOffDetail(off)}
}

// notifierOffDetail is the condition that says in whose configuration the notifier is
// off, empty when it is on everywhere that is read.
func notifierOffDetail(off []string) string {
	if len(off) == 0 {
		return ""
	}
	which := "profile "
	if len(off) > 1 {
		which = "profiles "
	}
	return detail(fmt.Sprintf("the Hermes gateway's Kanban notifier is disabled in the configuration of %s%s (%s is false): "+
		"a gateway started from it wakes no conversation, so replies are written to cards but nothing wakes the agent",
		which, strings.Join(off, ", "), kanban.NotifierSetting))
}

// toolsetsReading is what a verification found about the toolsets of the home
// conversation's agent; read is false when it did not look or could not tell.
type toolsetsReading struct {
	read   bool
	detail string // empty when every required toolset is enabled
}

// readToolsets reads which of the toolsets the agent delegates with are enabled for the
// home profile and platform. What cannot be read is logged and says nothing, so that the
// last answer stands.
func (g *gate) readToolsets(ctx context.Context) toolsetsReading {
	reading, err := g.board.Toolsets(ctx, g.homeProfile, g.homePlatform)
	if err != nil {
		if ctx.Err() == nil {
			g.log.Warn("could not read which toolsets the Hermes agent of the home conversation has",
				"profile", g.homeProfile, "platform", g.homePlatform, "error", err)
		}
		return toolsetsReading{}
	}
	return toolsetsReading{read: true, detail: toolsetsMissingDetail(g.homeProfile, g.homePlatform, reading.Missing)}
}

// toolsetsMissingDetail is the condition that says which toolsets the agent of a
// conversation lacks, empty when none is missing.
func toolsetsMissingDetail(profile, platform string, missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return detail(fmt.Sprintf("the Hermes agent of profile %s on platform %s has not enabled the toolset(s) %s: "+
		"it cannot delegate over Sidera or load the Sidera Skill (`sidera-adapter-hermes setup` prints the change to make)",
		profile, platform, strings.Join(missing, ", ")))
}

// verificationDue reports whether the next check has to be the whole probe.
func (g *gate) verificationDue() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.down || g.verifiedAt.IsZero() || g.now().Sub(g.verifiedAt) >= g.verifyEvery
}

// set records the outcome of a check and reports a change of what the daemon is told,
// and only a change: the daemon replaces the previous snapshot with each one it gets.
func (g *gate) set(ctx context.Context, err error, full bool, found reading) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case err != nil:
		g.verifiedAt = time.Time{}
	case full:
		g.verifiedAt = g.now()
	}
	if found.notifier.read {
		g.notifierOff = found.notifier.detail
	}
	if found.toolsets.read {
		g.toolsetsMissing = found.toolsets.detail
	}
	g.down = err != nil
	now := availability{down: g.down}
	if !g.down {
		now.notifier = g.notifierOff
		now.toolsets = g.toolsetsMissing
	}
	before := g.reported
	if now == before {
		return
	}
	g.reported = now

	snapshot := adapterproto.AvailabilityParams{Conditions: []adapterproto.Availability{}}
	if now.down {
		g.log.Warn("hermes kanban is not usable; receiving and polling are paused", "error", err)
		snapshot.Conditions = append(snapshot.Conditions, adapterproto.Availability{
			Condition: adapterproto.ConditionDependencyMissing,
			Detail:    detail(fmt.Sprintf("hermes kanban is not usable, so no card is read or written and messages wait in Sidera: %v", err)),
		})
	}
	for _, text := range []string{now.notifier, now.toolsets} {
		if text == "" {
			continue
		}
		g.log.Warn(text)
		snapshot.Conditions = append(snapshot.Conditions, adapterproto.Availability{
			Condition: adapterproto.ConditionDependencyMissing,
			Detail:    text,
		})
	}
	if before.down && !now.down {
		g.log.Info("hermes kanban is usable again")
	}
	if rerr := g.report(ctx, snapshot); rerr != nil && ctx.Err() == nil {
		g.log.Warn("reporting availability", "error", rerr)
	}
}

// detail cuts text to what an availability or dependency detail may hold.
func detail(s string) string {
	if len(s) <= adapterproto.MaxDetailLen {
		return s
	}
	return strings.ToValidUTF8(s[:adapterproto.MaxDetailLen-3], "") + "..."
}
