package store

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
)

// Recovery rules. Each rule has a predicate, a pure function of a Scan that
// names the work, and a method that does it. The predicate is the one thing the
// rule and the check "a run without a crash or damage has no recovery work" both
// call, so the check cannot drift from the rule. The predicates of the rules
// that need the delivery rules to decide what to write (draining records of a
// closed endpoint) return the candidates and the engine applies the transition.

// MissingStateDirs lists the directories of state/ that do not exist, parent
// first: the work of the layout rule.
func MissingStateDirs(r fsx.Reader) ([]string, error) {
	var out []string
	for _, d := range StateDirs() {
		exists, err := fsx.Exists(r, d)
		if err != nil {
			return nil, err
		}
		if !exists {
			out = append(out, d)
		}
	}
	return out, nil
}

// OrphanBodies is the work of orphan-gc: the bodies no record refers to. A body
// belongs to the record it is named after (BodyFileName), and is kept exactly
// while that record exists and holds a reference to a body: the body of a record
// that is gone is what a crash left between writing a body and its record, and
// the body of a record that no longer refers to one is what a crash left between
// writing the record and removing the body.
func OrphanBodies(sc *Scan) []string {
	refs := map[string]bool{}
	for _, o := range sc.Out {
		if o.Body != nil {
			refs[BodyFileName(o.Name())] = true
		}
	}
	for _, in := range sc.In {
		if in.Body != nil {
			refs[BodyFileName(in.Name())] = true
		}
	}
	for _, c := range sc.Ctl {
		if c.Body != nil {
			refs[BodyFileName(c.Name())] = true
		}
	}
	var out []string
	for _, b := range sc.Bodies {
		if !refs[b] {
			out = append(out, b)
		}
	}
	return out
}

// OrphanEndpoints is the work of orphan-gc-endpoint: the session endpoints
// still being created that no control record creates. The endpoint is
// preparation that never became part of a committed control record: its record
// is not there, or it is there and holds no creation of this endpoint (none at
// all, or the creation of another endpoint, which is what a signal accepted again
// after a failed intake has: the same record name and a new endpoint).
//
// A record that is damaged keeps the endpoint: the quarantine of the record
// settles it, as it needs to know it is there.
func OrphanEndpoints(sc *Scan) []EndpointRecord {
	ctls := map[string]*CtlRecord{}
	exists := map[string]bool{}
	for i := range sc.Ctl {
		c := &sc.Ctl[i]
		exists[c.Name()] = true
		ctls[c.Name()] = c
	}
	for _, d := range sc.Damaged {
		if d.Dir == CtlDir {
			exists[d.Name] = true
		}
	}
	var out []EndpointRecord
	for _, e := range sc.Endpoints {
		if e.Status != EndpointCreating {
			continue
		}
		name := e.CreatedBy + recordSuffix
		switch c := ctls[name]; {
		case e.CreatedBy == "" || !exists[name]:
			out = append(out, e)
		case c != nil && (c.Intent == nil || c.Intent.Endpoint != e.ID):
			out = append(out, e)
		}
	}
	return out
}

// QuarantineSessions is the session endpoints the quarantine of a control record
// closes, each as it is written: every session endpoint whose creator is the
// control record of the file name, but one that is open, with its token dropped.
// Several can name one record: a control record accepted again after a failed
// first acceptance has the same name and a new endpoint. An endpoint already
// closed is listed too, because what an interrupted quarantine wrote may not be
// durable. An open endpoint is a live session and stays open.
//
// Quarantine writes exactly this list for a control record. A caller asks for it
// only to learn whether a session may remain (the kind of the alert).
func QuarantineSessions(sc *Scan, ctlName string) []EndpointRecord {
	var out []EndpointRecord
	for _, e := range sc.Endpoints {
		if e.CreatedBy == "" || e.CreatedBy+recordSuffix != ctlName || e.Status == EndpointOpen {
			continue
		}
		e.Status, e.Token = EndpointClosed, nil
		out = append(out, e)
	}
	return out
}

// EffectReplays is the work of ctl-effect-replay, as the endpoint records to
// write: a session endpoint still being created whose control record holds the
// end of the creation becomes open or closed accordingly, and an open session
// endpoint a control record closed becomes closed. Nothing is ever moved from
// closed to open.
func EffectReplays(sc *Scan) []EndpointRecord {
	ctls := map[string]*CtlRecord{}
	closedBy := map[ident.EndpointID]bool{}
	for i := range sc.Ctl {
		c := &sc.Ctl[i]
		ctls[c.Name()] = c
		if !c.ClosedEndpoint.IsZero() {
			closedBy[c.ClosedEndpoint] = true
		}
	}
	var out []EndpointRecord
	for _, e := range sc.Endpoints {
		switch {
		case e.Status == EndpointCreating && e.CreatedBy != "":
			c := ctls[e.CreatedBy+recordSuffix]
			if c == nil || c.Intent == nil || c.Intent.Endpoint != e.ID || c.Intent.State == IntentCreating {
				continue
			}
			n := e
			if c.Intent.State == IntentCreated {
				n.Status = EndpointOpen
				sess := SessionInfo{}
				if e.Session != nil {
					sess = *e.Session
				}
				sess.Preset, sess.NativeRef, sess.Process = c.Preset, c.Intent.NativeRef, c.Intent.Process
				n.Session = &sess
			} else {
				n.Status, n.Token = EndpointClosed, nil
			}
			out = append(out, n)
		case e.Status == EndpointOpen && closedBy[e.ID]:
			n := e
			n.Status, n.Token = EndpointClosed, nil
			out = append(out, n)
		}
	}
	return out
}

// DrainCandidates is the structural part of the work of endpoint-drain-replay:
// the target and control records still accepted, whose endpoint is
// closed or has no record. The engine decides, with the delivery rules, which
// of them a forced close changes.
func DrainCandidates(sc *Scan) (in []InRecord, ctl []CtlRecord) {
	alive := map[ident.EndpointID]bool{}
	for _, e := range sc.Endpoints {
		if e.Status != EndpointClosed {
			alive[e.ID] = true
		}
	}
	for _, r := range sc.In {
		if r.Record.State == delivery.StateAccepted && !alive[r.Key.Recipient] {
			in = append(in, r)
		}
	}
	for _, r := range sc.Ctl {
		if r.Record.State == delivery.StateAccepted && !alive[r.Key.Recipient] {
			ctl = append(ctl, r)
		}
	}
	return in, ctl
}

// InstanceDrainWork is the work of instance-drain-replay: the endpoints, open or
// being created, of instances for which enabled is false, as closed records,
// token removed.
func InstanceDrainWork(sc *Scan, enabled func(instance string) bool) []EndpointRecord {
	var out []EndpointRecord
	for _, e := range sc.Endpoints {
		if e.Status != EndpointClosed && !enabled(e.Instance) {
			n := e
			n.Status, n.Token = EndpointClosed, nil
			out = append(out, n)
		}
	}
	return out
}

// coversByFile lists the feedback items the notices of a scan already cover, by
// the name of the file of out/ that holds each: the one place a cover is read,
// for source records and for out-lost records alike.
func coversByFile(sc *Scan) map[string][]CoverRef {
	by := map[string][]CoverRef{}
	for _, n := range sc.In {
		for _, c := range n.Covers {
			by[c.Out] = append(by[c.Out], c)
		}
	}
	return by
}

// withoutCovered is items without the ones a cover names: an item the cover has
// the recipient and the kind of.
func withoutCovered(items []delivery.Item, covers []CoverRef) []delivery.Item {
	return slices.DeleteFunc(slices.Clone(items), func(it delivery.Item) bool {
		return slices.ContainsFunc(covers, func(c CoverRef) bool {
			return it.Ref.Recipient == c.Recipient && it.Feedback.Kind == c.Kind
		})
	})
}

// FeedbackReplays is the work of feedback-replay, as the source records to
// write: those whose pending feedback holds items a notice already covers,
// without those items.
func FeedbackReplays(sc *Scan) []OutRecord {
	covers := coversByFile(sc)
	var out []OutRecord
	for _, o := range sc.Out {
		if items := withoutCovered(o.Feedback, covers[o.Name()]); len(items) != len(o.Feedback) {
			o.Feedback = items
			out = append(out, o)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name() < out[b].Name() })
	return out
}

// OutLostReplays is the work of feedback-replay for the out-lost records: those
// whose feedback item a notice already covers, without it. The record then has
// nothing pending and the sweep takes it.
func OutLostReplays(sc *Scan) []OutLostRecord {
	covers := coversByFile(sc)
	var out []OutLostRecord
	for _, o := range sc.OutLost {
		if items := withoutCovered(o.Feedback, covers[o.Name()]); len(items) != len(o.Feedback) {
			o.Feedback = items
			out = append(out, o)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name() < out[b].Name() })
	return out
}

// OrphanGC removes the bodies, then syncs the directory.
func (s *Store) OrphanGC(bodies []string) error {
	if len(bodies) == 0 {
		return nil
	}
	for _, b := range bodies {
		if !isBodyName(b) {
			return fmt.Errorf("store: %q is not the name of a body", b)
		}
	}
	files := make([]FileRef, len(bodies))
	for i, b := range bodies {
		files[i] = FileRef{Dir: BodiesDir, Name: b}
	}
	ex, err := s.exist(files)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declOrphanGC)
	defer tx.Close()
	for _, f := range files {
		if _, err := s.removeRef(tx, "body", f, ex); err != nil {
			return err
		}
	}
	if err := tx.SyncDir("dir", BodiesDir); err != nil {
		return err
	}
	return tx.Commit()
}

// OrphanGCEndpoint removes the endpoint records, then syncs the directory.
func (s *Store) OrphanGCEndpoint(eps []EndpointRecord) error {
	if len(eps) == 0 {
		return nil
	}
	files := make([]FileRef, len(eps))
	for i, e := range eps {
		files[i] = FileRef{Dir: EndpointsDir, Name: e.Name()}
	}
	ex, err := s.exist(files)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declOrphanGCEndpoint)
	defer tx.Close()
	for _, f := range files {
		if _, err := s.removeRef(tx, "endpoint", f, ex); err != nil {
			return err
		}
	}
	if err := tx.SyncDir("dir", EndpointsDir); err != nil {
		return err
	}
	return tx.Commit()
}

// CtlEffectReplay writes the endpoint records EffectReplays returned.
func (s *Store) CtlEffectReplay(eps []EndpointRecord) error {
	if len(eps) == 0 {
		return nil
	}
	var files []file
	for _, e := range eps {
		f, err := recordFile(EndpointsDir, e)
		if err != nil {
			return err
		}
		files = append(files, f)
	}
	tx := s.w.Begin(&declCtlEffectReplay)
	defer tx.Close()
	if err := writeGroup(tx, "endpoint", "endpoint-dir", EndpointsDir, files); err != nil {
		return err
	}
	return tx.Commit()
}

// EndpointDrainReplay writes a chunk of the closed deliveries of closed
// endpoints.
// It returns the target records as written (the revision one on).
func (s *Store) EndpointDrainReplay(b DrainBatch) ([]InRecord, error) {
	return s.drain(&declEndpointDrain, nil, b, false)
}

// InstanceDrainReplay closes the endpoints of disabled instances and writes a
// chunk of their deliveries.
// It returns the target records as written (the revision one on).
func (s *Store) InstanceDrainReplay(closed []EndpointRecord, b DrainBatch) ([]InRecord, error) {
	return s.drain(&declInstanceDrain, closed, b, true)
}

// FeedbackReplay writes the source records without the feedback a notice covers.
func (s *Store) FeedbackReplay(outs []OutRecord) error {
	if len(outs) == 0 {
		return nil
	}
	return s.FeedbackClear(outs)
}

// OutLostClear writes the out-lost records without the feedback item a notice
// covers, as FeedbackClear does for source records, and as OutLostReplays asks
// after a crash.
func (s *Store) OutLostClear(lost []OutLostRecord) error {
	if len(lost) == 0 {
		return errors.New("store: OutLostClear needs a record")
	}
	var files []file
	for _, o := range lost {
		f, err := recordFile(OutDir, o)
		if err != nil {
			return err
		}
		files = append(files, f)
	}
	tx := s.w.Begin(&declFeedbackClear)
	defer tx.Close()
	if err := writeGroup(tx, "out", "out-dir", OutDir, files); err != nil {
		return err
	}
	return tx.Commit()
}
