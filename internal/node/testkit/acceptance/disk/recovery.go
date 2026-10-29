// Package disk owns execution-neutral acceptance operations on real node files.
package disk

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/txn"
)

// DamageResult identifies the actual product-created objects changed offline.
type DamageResult struct{ Endpoint, Body string }

// Damage changes an existing record, retained body or empty layout directory.
// The caller must stop the node first and must not count these writes as evidence.
func Damage(d fsx.FS, tr crashwalk.Trigger) (DamageResult, error) {
	if tr.Kind != crashwalk.TriggerCorrupt {
		return DamageResult{}, fmt.Errorf("not offline damage: %+v", tr)
	}
	switch tr.Pattern {
	case "source-body":
		if tr.Damage != "flip-body" {
			return DamageResult{}, fmt.Errorf("unknown body damage %q", tr.Damage)
		}
		return damageBody(d)
	case "state/checkpoints":
		if tr.Damage != "remove-empty-directory" {
			return DamageResult{}, fmt.Errorf("unknown layout damage %q", tr.Damage)
		}
		entries, err := d.ReadDir(tr.Pattern)
		if err != nil {
			return DamageResult{}, err
		}
		if len(entries) != 0 {
			return DamageResult{}, errors.New("layout damage requires an actual empty directory")
		}
		return DamageResult{}, fsx.RemoveAll(d, fsx.Tag{Tx: "offline", Role: "directory"}, tr.Pattern)
	default:
		return damageRecord(d, tr)
	}
}

func damageRecord(d fsx.FS, tr crashwalk.Trigger) (DamageResult, error) {
	var original store.EndpointRecord
	var result DamageResult
	sc, err := readRecords(d)
	if err != nil {
		return result, err
	}
	var file string
	switch tr.Pattern {
	case "source":
		if len(sc.Out) != 1 {
			return result, fmt.Errorf("damage requires one actual source, got %d", len(sc.Out))
		}
		file = path.Join(store.OutDir, sc.Out[0].Name())
	case "target":
		if len(sc.In) != 1 {
			return result, fmt.Errorf("damage requires one actual target, got %d", len(sc.In))
		}
		file = path.Join(store.InDir, sc.In[0].Name())
	case "creating-control":
		if len(sc.Ctl) != 1 || sc.Ctl[0].Intent == nil {
			return result, errors.New("damage requires a real creation intent")
		}
		for _, ep := range sc.Endpoints {
			if ep.Status == store.EndpointCreating && ep.CreatedBy+".rec" == sc.Ctl[0].Name() {
				original = ep
			}
		}
		if original.ID.IsZero() {
			return result, errors.New("clean shutdown did not preserve a creating endpoint")
		}
		file = path.Join(store.CtlDir, sc.Ctl[0].Name())
	default:
		return result, fmt.Errorf("unknown recovery damage pattern %q", tr.Pattern)
	}
	data, err := d.ReadFile(file)
	if err != nil {
		return result, err
	}
	switch tr.Damage {
	case "preserve-header":
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			return result, errors.New("actual source has no header")
		}
		data = append(append([]byte(nil), data[:newline+1]...), []byte("damaged payload")...)
	case "unreadable":
		data = []byte("unreadable actual record")
	default:
		return result, fmt.Errorf("unknown recovery damage %q", tr.Damage)
	}
	if err := fsx.Replace(d, fsx.Tag{Tx: "offline", Role: "record"}, file, data, fsx.ModePrivate); err != nil {
		return result, err
	}
	if !original.ID.IsZero() {
		result.Endpoint = original.ID.String()
	}
	return result, nil
}

func damageBody(d fsx.FS) (DamageResult, error) {
	scan, err := readRecords(d)
	if err != nil {
		return DamageResult{}, err
	}
	if len(scan.Out) != 1 || scan.Out[0].Body == nil || !scan.Out[0].NeedsBody() {
		return DamageResult{}, fmt.Errorf("body damage requires one actual source retaining its body")
	}
	file := path.Join(store.BodiesDir, store.BodyFileName(scan.Out[0].Name()))
	data, err := d.ReadFile(file)
	if err != nil {
		return DamageResult{}, err
	}
	if len(data) == 0 || store.BodyRefOf(data) != *scan.Out[0].Body {
		return DamageResult{}, fmt.Errorf("body damage requires the actual intact source body")
	}
	data[0] ^= 1
	if err := fsx.Replace(d, fsx.Tag{Tx: "offline", Role: "body"}, file, data, fsx.ModePrivate); err != nil {
		return DamageResult{}, err
	}
	return DamageResult{Body: file}, nil
}

// CheckLayout requires the actual daemon layout to be fully repaired.
func CheckLayout(r fsx.Reader) error {
	missing, err := daemon.LayoutWork(r)
	if err != nil {
		return err
	}
	if len(missing) != 0 {
		return fmt.Errorf("unrepaired layout %v", missing)
	}
	return nil
}

// CheckClosed audits owner predicates and live delivery obligations after draining.
func CheckClosed(r fsx.Reader, sc *store.Scan, now time.Time, enabled func(string) bool) error {
	if plan := engine.PlanRecovery(sc, now, enabled); !plan.Empty() {
		return fmt.Errorf("unsettled recovery work: %v", plan.Items())
	}
	if work, err := txn.CleanupWork(r); err != nil || len(work) != 0 {
		return fmt.Errorf("unsettled transaction leftovers: %v: %v", work, err)
	}
	if work, err := config.AdapterGCWork(r); err != nil || len(work) != 0 {
		return fmt.Errorf("unsettled adapter garbage: %v: %v", work, err)
	}
	if len(sc.Damaged) != 0 || len(sc.Unknown) != 0 {
		return fmt.Errorf("unsettled disk damage=%v unknown=%v", sc.Damaged, sc.Unknown)
	}
	for _, ep := range sc.Endpoints {
		if ep.Status == store.EndpointCreating {
			return fmt.Errorf("unsettled creating endpoint %s", ep.ID)
		}
	}
	for _, c := range sc.Ctl {
		if c.Record.State == delivery.StateQueued || c.Record.State == delivery.StateAccepted {
			return fmt.Errorf("unsettled control %s", c.Record.State)
		}
	}
	for _, in := range sc.In {
		if in.Record.State == delivery.StateQueued || in.Record.State == delivery.StateAccepted {
			return fmt.Errorf("unsettled target %+v", in)
		}
	}
	for _, out := range sc.Out {
		if len(out.Feedback) != 0 {
			return errors.New("unsettled source feedback")
		}
		{
			for _, recipient := range out.Outbound.Recipients {
				if recipient.Delivery.State == delivery.StateQueued || recipient.Delivery.State == delivery.StateAccepted {
					return fmt.Errorf("unsettled source %s", recipient.Delivery.State)
				}
			}
		}
	}
	return nil
}

// Read only the real records needed to select offline damage.
func readRecords(r fsx.Reader) (*store.Scan, error) {
	sc := &store.Scan{}
	for _, dir := range []string{store.OutDir, store.InDir, store.CtlDir, store.EndpointsDir} {
		entries, err := r.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir || fsx.IsLeftover(entry.Name) {
				continue
			}
			data, err := r.ReadFile(path.Join(dir, entry.Name))
			if err != nil {
				return nil, err
			}
			rec, damage := store.DecodeRecord(dir, entry.Name, data)
			if damage != nil {
				return nil, fmt.Errorf("preexisting damage: %+v", damage)
			}
			switch v := rec.(type) {
			case store.OutRecord:
				sc.Out = append(sc.Out, v)
			case store.InRecord:
				sc.In = append(sc.In, v)
			case store.CtlRecord:
				sc.Ctl = append(sc.Ctl, v)
			case store.EndpointRecord:
				sc.Endpoints = append(sc.Endpoints, v)
			}
		}
	}
	return sc, nil
}
