package crashwalk

import (
	"fmt"
	"slices"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// CoveragePartition assigns every declared key to this layer or another layer.
// Both lists must be duplicate-free, disjoint and together cover the full table.
// It supplies responsibility only; coverage still comes from this walk's runs.
type CoveragePartition struct {
	Required         []fsx.Key
	CoveredElsewhere []fsx.Key
}

func coveragePartition(table *txn.Table, input *CoveragePartition, hasScenarios bool) (*CoveragePartition, map[fsx.Key]bool, error) {
	declared := map[fsx.Key]bool{}
	for _, k := range table.Keys() {
		declared[k.Key] = true
	}
	if input == nil {
		return nil, declared, nil
	}
	partition := &CoveragePartition{Required: slices.Clone(input.Required), CoveredElsewhere: slices.Clone(input.CoveredElsewhere)}
	required := map[fsx.Key]bool{}
	classified := map[fsx.Key]string{}
	for _, part := range []struct {
		name string
		keys []fsx.Key
	}{{"Required", partition.Required}, {"CoveredElsewhere", partition.CoveredElsewhere}} {
		for _, k := range part.keys {
			if !declared[k] {
				return nil, nil, fmt.Errorf("crashwalk: coverage %s contains undeclared key %v", part.name, k)
			}
			if previous := classified[k]; previous != "" {
				return nil, nil, fmt.Errorf("crashwalk: coverage key %v appears in both %s and %s; every key must appear exactly once", k, previous, part.name)
			}
			classified[k] = part.name
			if part.name == "Required" {
				required[k] = true
			}
		}
	}
	var missing []fsx.Key
	for _, k := range table.Keys() {
		if classified[k.Key] == "" {
			missing = append(missing, k.Key)
		}
	}
	if len(missing) > 0 {
		return nil, nil, missingWrites("coverage partition leaves declared keys unclassified", missing)
	}
	if hasScenarios && len(required) == 0 {
		return nil, nil, fmt.Errorf("crashwalk: coverage Required is empty for a nonempty scenario catalog")
	}
	return partition, required, nil
}

func requiredKeys(keys []fsx.Key, required map[fsx.Key]bool) []fsx.Key {
	var out []fsx.Key
	for _, k := range keys {
		if required[k] {
			out = append(out, k)
		}
	}
	return out
}
