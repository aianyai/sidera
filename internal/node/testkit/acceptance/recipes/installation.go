package recipes

import (
	"errors"
	"fmt"
	"slices"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

// PackageSpec describes input files, never node state.
type PackageSpec struct {
	ID, Version string
	NestedFile  bool
	Unsupported bool
}

type InstallOutcome struct {
	ID, Version              string
	AlreadyCurrent, Replayed bool
	// Code is the observed recoverable failure; execution failures remain errors.
	Code string
}

type InstalledPackage struct {
	Current  string
	Versions []string
}

// InstallOps applies an installation request and checks its persistent replay
// before returning the first result. Inspection reads only package state.
type InstallOps struct {
	Install     func(scenario.Env, admin.OperationID, PackageSpec) (InstallOutcome, error)
	FailPointer func(scenario.Env, bool) error
	Inspect     func(scenario.Env, string) (InstalledPackage, error)
}

var ErrInstallationEffect = errors.New("acceptance: installation result and effect disagree")

func installationRecipes(b Bindings) []ScriptRecipe {
	const id = "acceptance-package"
	v1 := PackageSpec{ID: id, Version: "1.0.0"}
	v2 := PackageSpec{ID: id, Version: "2.0.0", NestedFile: true}
	install := func(e scenario.Env, request byte, spec PackageSpec, current bool) error {
		out, err := b.Installation.Install(e, admin.OperationID{request}, spec)
		if err != nil {
			return err
		}
		if out.Code != "" || out.ID != spec.ID || out.Version != spec.Version || out.AlreadyCurrent != current {
			return fmt.Errorf("%w: install %s returned %+v", ErrInstallationEffect, spec.Version, out)
		}
		return checkInstalled(e, b.Installation, id, spec.Version, []string{spec.Version})
	}
	failed := func(cleanup bool, request byte) func(scenario.Env) error {
		return func(e scenario.Env) error {
			if err := b.Installation.FailPointer(e, cleanup); err != nil {
				return err
			}
			out, err := b.Installation.Install(e, admin.OperationID{request}, v2)
			if err != nil {
				return err
			}
			if out.Code != "pointer_failed" || out.Replayed {
				return fmt.Errorf("%w: failed pointer returned %+v", ErrInstallationEffect, out)
			}
			versions := []string{v1.Version}
			if cleanup {
				versions = append(versions, v2.Version)
			}
			return checkInstalled(e, b.Installation, id, v1.Version, versions)
		}
	}
	return []ScriptRecipe{{
		Script: scenario.Script{Name: "installation-rollback", Crash: scenario.CrashPlan{MaxPoints: 162, Recovery: []crashwalk.RecoveryCase{
			{Item: fsx.Key{Tag: fsx.Tag{Tx: "adapter-gc", Role: "dir"}, Step: fsx.StepRenameDir}, After: crashwalk.CrashAt(fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "adapter-stage", Role: "file"}, Step: fsx.StepWrite}, Hit: 1})},
			{Item: fsx.Key{Tag: fsx.Tag{Tx: "adapter-gc", Role: "dir"}, Step: fsx.StepRemoveAll}, After: crashwalk.CrashAt(fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "adapter-stage", Role: "file"}, Step: fsx.StepWrite}, Hit: 1})},
			{Item: fsx.Key{Tag: fsx.Tag{Tx: "adapter-gc"}, Step: fsx.StepCommit}, After: crashwalk.CrashAt(fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "adapter-stage", Role: "file"}, Step: fsx.StepWrite}, Hit: 1})},
			{Item: fsx.Key{Tag: fsx.Tag{Tx: "txn-cleanup", Role: "trash"}, Step: fsx.StepRemoveAll}, After: crashwalk.CrashAt(fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "adapter-unstage", Role: "stage"}, Step: fsx.StepRemoveAll}, Hit: 1})},
		}}, Steps: []scenario.Step{
			{Name: "install-first-version", Do: func(e scenario.Env) error { return install(e, 31, v1, false) }},
			{Name: "rollback-failed-pointer", Do: failed(false, 32)},
			{Name: "leave-failed-cleanup", Do: failed(true, 33)},
			{Name: "discard-stale-and-upgrade", Do: func(e scenario.Env) error { return install(e, 34, v2, false) }},
			{Name: "install-current-version", Do: func(e scenario.Env) error { return install(e, 35, v2, true) }},
			{Name: "reject-unsupported-package", Do: func(e scenario.Env) error {
				spec := PackageSpec{ID: "acceptance-foreign", Version: "1.0.0", Unsupported: true}
				out, err := b.Installation.Install(e, admin.OperationID{36}, spec)
				if err != nil {
					return err
				}
				if out.Code != "platform_unsupported" {
					return fmt.Errorf("%w: unsupported package returned %+v", ErrInstallationEffect, out)
				}
				return checkInstalled(e, b.Installation, spec.ID, "", nil)
			}},
		}},
		Drain: func(e scenario.Env) error {
			out, err := b.Installation.Install(e, admin.OperationID{34}, v2)
			if err != nil {
				return err
			}
			if out.Code != "" || out.ID != id || out.Version != v2.Version {
				return fmt.Errorf("%w: final installation %+v", ErrInstallationEffect, out)
			}
			return nil
		},
		Check: func(e scenario.Env) error {
			return checkInstalled(e, b.Installation, id, v2.Version, []string{v2.Version})
		},
	}}
}

func checkInstalled(e scenario.Env, ops InstallOps, id, current string, versions []string) error {
	got, err := ops.Inspect(e, id)
	if err != nil {
		return err
	}
	if got.Current != current || !slices.Equal(got.Versions, versions) {
		return fmt.Errorf("%w: package %s current=%s versions=%v, want current=%s versions=%v", ErrInstallationEffect, id, got.Current, got.Versions, current, versions)
	}
	return nil
}
