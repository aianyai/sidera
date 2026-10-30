package acceptance

import (
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
)

// recoveryState belongs to one run, including all of its process generations.
type recoveryState struct {
	kind    recipes.DamageKind
	applied bool
	result  disk.DamageResult
}

func (s *recoveryState) apply(f *Fixture, tr crashwalk.Trigger) error {
	if f.current != nil && !f.current.p.exited() {
		return errors.New("acceptance: offline damage requires process exit")
	}
	if tr.Pattern != string(s.kind) {
		return fmt.Errorf("acceptance: undeclared damage %q, want %q", tr.Pattern, s.kind)
	}
	d, err := nodeDisk(f)
	if err != nil {
		return err
	}
	result, err := disk.Damage(d, tr)
	if err == nil {
		s.result, s.applied = result, true
	}
	return err
}
func nodeDisk(f *Fixture) (fsx.FS, error) { return fsx.OpenOS(f.home, rand.Reader) }
func scanDisk(f *Fixture) (fsx.FS, *store.Scan, error) {
	d, err := nodeDisk(f)
	if err != nil {
		return nil, nil, err
	}
	// Scan is read-only; neither Config.Load nor a recovery entry is called.
	scan, err := store.New(newReadWriter(f, d)).Scan()
	return d, scan, err
}
func (s *recoveryState) ops() recipes.RecoveryOps {
	return recipes.RecoveryOps{
		InspectDamage: func(e scenario.Env) (recipes.DamageFacts, error) {
			if !s.applied {
				return recipes.DamageFacts{}, nil
			}
			env := envFrom(e)
			if err := env.check(); err != nil {
				return recipes.DamageFacts{}, err
			}
			_, scan, err := scanDisk(env.fixture)
			if err != nil {
				return recipes.DamageFacts{}, err
			}
			return disk.InspectDamage(scan, s.result), nil
		},
		CheckClosedRecords: func(e scenario.Env) error {
			env := envFrom(e)
			if err := env.check(); err != nil {
				return err
			}
			d, scan, err := scanDisk(env.fixture)
			if err != nil {
				return err
			}
			raw, err := d.ReadFile(config.InstancesPath)
			if err != nil {
				return err
			}
			instances, err := config.DecodeInstances(raw)
			if err != nil {
				return err
			}
			return disk.CheckClosed(d, scan, env.fixture.Now(), func(name string) bool { return instances.Items[name].Enabled })
		},
	}
}

func newReadWriter(f *Fixture, d fsx.FS) *txn.Writer { return txn.New(d, f.table, nil) }
