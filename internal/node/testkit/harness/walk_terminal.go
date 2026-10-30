package harness

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/diskfacts"
	"github.com/aianyai/sidera/internal/node/txn"
)

// TerminalOperation selects a management request that ends the real daemon.
type TerminalOperation uint8

const (
	TerminalStop TerminalOperation = iota + 1
	TerminalRetire
)

func (o TerminalOperation) String() string {
	switch o {
	case TerminalStop:
		return "daemon.stop"
	case TerminalRetire:
		return "identity.retire"
	default:
		return "unknown"
	}
}

// TerminalAudit is an additional attempt after the walk chain is terminal. Its
// observations and exit are evidence for Check, never coverage or target hits.
type TerminalAudit struct {
	Attempted, Started, IdentityRetired, SigningUnchanged bool
	Trace                                                 []crashwalk.Hit
	StartError, ExitError                                 error
	Query, Replay                                         json.RawMessage
}

// TerminalFacts contains only actual lifecycle and stopped-disk observations.
// Response is absent unless a complete management answer was received. Persisted
// is read independently; an interrupted request never supplies a guessed result.
type TerminalFacts struct {
	Node                                                ident.NodeID
	RequestID                                           admin.OperationID
	Response, Persisted                                 json.RawMessage
	Exited, Retired, RecoveryRejected, SigningUnchanged bool
	Audit                                               *TerminalAudit
}

// TerminalWalkScenario owns real daemon startup, a terminal management request,
// its exit and subsequent verification. It declares no after-return boundary.
type TerminalWalkScenario struct {
	Label       string
	Operation   TerminalOperation
	RequestID   admin.OperationID
	PointBudget int
	Check       func(TerminalFacts) error
}

func (s *TerminalWalkScenario) Name() string                       { return s.Label }
func (s *TerminalWalkScenario) Steps() []string                    { return nil }
func (s *TerminalWalkScenario) MaxPoints() int                     { return s.PointBudget }
func (s *TerminalWalkScenario) Recovery() []crashwalk.RecoveryCase { return nil }
func (s *TerminalWalkScenario) validate() error {
	if s.Label == "" || s.PointBudget <= 0 || s.RequestID.IsZero() {
		return errors.New("harness: terminal walk requires a name, positive budget and request identifier")
	}
	if s.Operation != TerminalStop && s.Operation != TerminalRetire {
		return errors.New("harness: unknown terminal operation")
	}
	return nil
}

func runTerminal(ctx context.Context, t *walkTest, s *TerminalWalkScenario, r *walkRun) error {
	checkNoLeaks(t, append(append([]string(nil), ownFrames...), "internal/node/daemon."), leakWait)
	home, err := startupHome(t)
	if err != nil {
		return err
	}
	disk := fsxtest.NewMem()
	clk := clocktest.NewFake(DefaultStart)
	if _, err := daemon.Init(ctx, daemon.Config{Home: home, FS: disk, Clock: clk, Rand: &startupRandom{r: RandReader(keySeed)}}); err != nil {
		return err
	}
	k, err := keys.Load(txn.New(disk, ProgramTable(t), nil))
	if err != nil {
		return err
	}
	facts := TerminalFacts{Node: k.Node(), RequestID: s.RequestID}
	request := manageapi.Request{OperationID: s.RequestID}
	if s.Operation == TerminalRetire {
		request.Params = map[string]string{"node": facts.Node.String()}
	}
	signing, err := diskfacts.ReadSigning(disk)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		g := r.begin(nil)
		if g.gen > 1 {
			g.openWindow()
		}
		attemptCtx, cancel := context.WithCancel(ctx)
		a := &startupAttempt{generation: g, disk: disk, cut: newCutoff(disk), cancel: cancel}
		d, startErr := daemon.Start(attemptCtx, daemon.Config{Home: home, FS: a.cut, Clock: clk, Rand: &startupRandom{r: RandReader(keySeed + uint64(g.gen))}, Observer: a})
		var callErr, exitErr error
		if startErr == nil {
			if _, err := d.Engine().Quiesce(attemptCtx, engine.Expect{}); err != nil {
				callErr = err
			} else {
				g.openWindow()
				var c *manageapi.Client
				c, callErr = manageapi.NewClient(manageapi.ClientConfig{Home: home})
				if callErr == nil {
					var response json.RawMessage
					response, callErr = c.Call(attemptCtx, s.Operation.String(), request)
					if callErr == nil {
						facts.Response = bytes.Clone(response)
					}
					c.Close()
				}
			}
			if callErr != nil {
				d.RequestStop("terminal walk request ended")
			}
			exitErr = d.Wait()
		}
		cancel()
		snapshot := a.snapshot()
		crashed := snapshot != nil
		if errors.Is(exitErr, daemon.ErrEngineNotStopped) || errors.Is(startErr, daemon.ErrEngineNotStopped) {
			return errors.Join(startErr, exitErr)
		}
		r.finishGeneration()
		if crashed {
			disk = snapshot
			continue
		}
		if startErr != nil {
			if s.Operation != TerminalRetire || g.gen == 1 || !errors.Is(startErr, keys.ErrIdentityRetired) {
				return startErr
			}
			facts.RecoveryRejected = true
			facts.SigningUnchanged, err = signing.Unchanged(disk)
			if err != nil {
				return err
			}
		} else {
			if callErr != nil || exitErr != nil {
				return errors.Join(callErr, exitErr)
			}
			facts.Exited = true
		}
		break
	}
	if err := r.checkTrigger(); err != nil {
		return err
	}
	scan, err := store.New(txn.New(disk, ProgramTable(t), nil)).Scan()
	if err != nil {
		return err
	}
	terminal, err := diskfacts.InspectTerminal(disk, scan, facts.Node, s.RequestID)
	if err != nil {
		return err
	}
	facts.Persisted, facts.Retired = terminal.Persisted, terminal.Retired
	if !facts.RecoveryRejected {
		facts.Audit, err = auditTerminal(ctx, t, home, disk, clk, s, request, signing, len(r.result.Hits)+1)
		if err != nil {
			r.result.Violations = append(r.result.Violations, err.Error())
		}
	}
	if s.Check != nil {
		if err := s.Check(facts); err != nil {
			r.result.Violations = append(r.result.Violations, err.Error())
		}
	}
	return nil
}

func auditTerminal(ctx context.Context, t *walkTest, home string, disk *fsxtest.Mem, clk *clocktest.Fake, s *TerminalWalkScenario, request manageapi.Request, signing diskfacts.SigningSnapshot, attempt int) (*TerminalAudit, error) {
	audit := &TerminalAudit{Attempted: true}
	g := newWalkGeneration(attempt, nil)
	g.openWindow()
	defer g.close()
	d, err := daemon.Start(ctx, daemon.Config{Home: home, FS: disk, Clock: clk, Rand: &startupRandom{r: RandReader(keySeed + uint64(attempt))}, Observer: g})
	audit.StartError = err
	audit.IdentityRetired = errors.Is(err, keys.ErrIdentityRetired)
	if audit.IdentityRetired {
		err = nil
	}
	if audit.StartError == nil {
		audit.Started = true
		var clientErr error
		if s.Operation == TerminalStop {
			c, openErr := manageapi.NewClient(manageapi.ClientConfig{Home: home})
			clientErr = openErr
			if clientErr == nil {
				var raw json.RawMessage
				raw, clientErr = c.Call(ctx, "operation.result", manageapi.Request{Params: map[string]string{"operation_id": hex.EncodeToString(s.RequestID[:])}})
				if clientErr == nil {
					audit.Query = bytes.Clone(raw)
					audit.Replay, clientErr = c.Call(ctx, s.Operation.String(), request)
				}
				c.Close()
			}
		}
		if clientErr != nil || s.Operation != TerminalStop {
			d.RequestStop("terminal lifecycle audit complete")
		}
		audit.ExitError = d.Wait()
		err = errors.Join(clientErr, audit.ExitError)
	}
	g.close()
	audit.Trace = g.recording()
	audit.SigningUnchanged, err = terminalAuditSigning(disk, signing, err)
	t.Logf("phase=audit scenario=%s attempt=%d started=%v identity_retired=%v start_error=%v exit_error=%v hits=%d", s.Label, attempt, audit.Started, audit.IdentityRetired, audit.StartError, audit.ExitError, len(audit.Trace))
	for _, h := range audit.Trace {
		t.Logf("phase=audit scenario=%s attempt=%d point=%s path=%q from=%q", s.Label, attempt, h.Point, h.Path, h.From)
	}
	coverage := crashwalk.Assess(ProgramTable(t), audit.Trace)
	if len(coverage.Undeclared) != 0 {
		return audit, fmt.Errorf("harness: terminal audit has undeclared writes: %v", coverage.Undeclared)
	}
	return audit, err
}
func terminalAuditSigning(r fsx.Reader, before diskfacts.SigningSnapshot, cause error) (bool, error) {
	unchanged, err := before.Unchanged(r)
	return unchanged, errors.Join(cause, err)
}
