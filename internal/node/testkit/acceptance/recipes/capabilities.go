package recipes

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/txn"
)

// DamageKind names an offline operation on product-created files.
type DamageKind string

const (
	DamageSource          DamageKind = "source"
	DamageTarget          DamageKind = "target"
	DamageCreatingControl DamageKind = "creating-control"
	DamageSourceBody      DamageKind = "source-body"
	DamageLayout          DamageKind = "state/checkpoints"
)

// FSFault names a required in-process filesystem failure capability.
type FSFault string

const (
	FaultSourceUpdate     FSFault = "source-update"
	FaultRetainLostBody   FSFault = "retain-lost-source-body"
	FaultCreateSettlement FSFault = "create-settlement"
	FaultInstallPointer   FSFault = "install-pointer"
)

// Needs declares fixture inputs before the recipe can execute any callback.
type Needs struct {
	RuntimeAdapter    bool
	ExtraInstances    []string
	ArchiveSweepEvery time.Duration
	PackageInput      bool

	// ProbeWarning requires an installed, disabled adapter whose probe reports a
	// runtime version outside its manifest's tested range. The recipe enables it.
	ProbeWarning bool

	Damage   DamageKind
	FSFaults []FSFault
}

// Capabilities describes what an execution layer can supply.
type Capabilities struct {
	RuntimeAdapter, ExtraInstances, ArchiveSweep, PackageInput, ProbeWarning bool

	Damage   []DamageKind
	FSFaults []FSFault
}

// BinaryCapabilities intentionally provides no filesystem fault injection.
func BinaryCapabilities() Capabilities {
	return Capabilities{RuntimeAdapter: true, ExtraInstances: true, ArchiveSweep: true, PackageInput: true, ProbeWarning: true,
		Damage: []DamageKind{DamageSource, DamageTarget, DamageCreatingControl, DamageSourceBody, DamageLayout}}
}

// Supports selects from declarations, before a callback can fail at runtime.
func (c Capabilities) Supports(n Needs) bool {
	switch n.Damage {
	case "", DamageSource, DamageTarget, DamageCreatingControl, DamageSourceBody, DamageLayout:
	default:
		return false
	}
	if n.RuntimeAdapter && !c.RuntimeAdapter || len(n.ExtraInstances) != 0 && !c.ExtraInstances || n.ArchiveSweepEvery != 0 && !c.ArchiveSweep || n.PackageInput && !c.PackageInput || n.ProbeWarning && !c.ProbeWarning || n.Damage != "" && !slices.Contains(c.Damage, n.Damage) {
		return false
	}
	for _, fault := range n.FSFaults {
		switch fault {
		case FaultSourceUpdate, FaultRetainLostBody, FaultCreateSettlement, FaultInstallPointer:
		default:
			return false
		}
		if !slices.Contains(c.FSFaults, fault) {
			return false
		}
	}
	return true
}

// Select retains only recipes whose declared needs the layer can supply.
func (c Catalog) Select(capabilities Capabilities) Catalog {
	var selected Catalog
	for _, r := range c.Scripts {
		if capabilities.Supports(r.Needs) {
			selected.Scripts = append(selected.Scripts, r)
		}
	}
	for _, r := range c.Startups {
		if capabilities.Supports(r.Needs) {
			selected.Startups = append(selected.Startups, r)
		}
	}
	for _, r := range c.Terminals {
		if capabilities.Supports(r.Needs) {
			selected.Terminals = append(selected.Terminals, r)
		}
	}

	return selected
}

// LayerEvidence keeps actual reachable keys and recovery targets with their
// source recipe and trigger. InProcessOnly is the observed full-catalog difference.
type LayerEvidence struct {
	Analysis      crashwalk.SourceAnalysis
	Reachable     []fsx.Key
	InProcessOnly []fsx.Key
}

// AnalyzeLayer uses only complete recordings, already validated by crashwalk.
// A key is reachable if any supported recipe actually observed it. Recovery
// targets are recomputed from supported sources, rather than relabeling targets
// whose key happens to be reachable through some other recipe.
func (c Catalog) AnalyzeLayer(table *txn.Table, capabilities Capabilities, clean map[string][]crashwalk.Hit, recovered []crashwalk.RecoveryRecord) (LayerEvidence, error) {
	var out LayerEvidence
	needs := map[string]Needs{}
	add := func(name string, n Needs) error {
		if _, exists := needs[name]; exists {
			return fmt.Errorf("duplicate recipe %q", name)
		}
		needs[name] = n
		return nil
	}
	for _, r := range c.Scripts {
		if err := add(r.Script.Name, r.Needs); err != nil {
			return out, err
		}
	}
	for _, r := range c.Startups {
		if err := add(r.Name, r.Needs); err != nil {
			return out, err
		}
	}
	for _, r := range c.Terminals {
		if err := add(r.Name, r.Needs); err != nil {
			return out, err
		}
	}

	var allClean, selectedClean []crashwalk.Hit
	var selectedRecovery []crashwalk.RecoveryRecord
	allKeys, selectedKeys := map[fsx.Key]bool{}, map[fsx.Key]bool{}
	observe := func(hits []crashwalk.Hit, supported bool) {
		for _, h := range hits {
			allKeys[h.Point.Key] = true
			if supported {
				selectedKeys[h.Point.Key] = true
			}
		}
	}
	names := make([]string, 0, len(clean))
	for name := range clean {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		n, ok := needs[name]
		if !ok {
			return out, fmt.Errorf("unknown clean recipe %q", name)
		}
		supported := capabilities.Supports(n)
		allClean = append(allClean, clean[name]...)
		observe(clean[name], supported)
		if supported {
			selectedClean = append(selectedClean, clean[name]...)
		}
	}
	for _, r := range recovered {
		n, ok := needs[r.Scenario]
		if !ok {
			return out, fmt.Errorf("unknown recovery recipe %q", r.Scenario)
		}
		supported := capabilities.Supports(n)
		observe(r.Hits, supported)
		if supported {
			selectedRecovery = append(selectedRecovery, r)
		}
	}
	if _, err := crashwalk.AnalyzeSources(table, allClean, recovered); err != nil {
		return out, err
	}
	var err error
	out.Analysis, err = crashwalk.AnalyzeSources(table, selectedClean, selectedRecovery)
	if err != nil {
		return out, err
	}
	for key := range allKeys {
		if selectedKeys[key] {
			out.Reachable = append(out.Reachable, key)
		} else {
			out.InProcessOnly = append(out.InProcessOnly, key)
		}
	}
	for _, keys := range [][]fsx.Key{out.Reachable, out.InProcessOnly} {
		slices.SortFunc(keys, func(a, b fsx.Key) int {
			if a.String() < b.String() {
				return -1
			}
			if a == b {
				return 0
			}
			return 1
		})
	}
	return out, nil
}
