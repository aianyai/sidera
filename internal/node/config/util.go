package config

import (
	"cmp"
	"encoding/json"
	"slices"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func sortedEndpoints[V any](m map[ident.EndpointID]V) []ident.EndpointID {
	ids := make([]ident.EndpointID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b ident.EndpointID) int { return cmp.Compare(a.String(), b.String()) })
	return ids
}

func cloneRaw(m map[string]json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

func cloneOperation(o *Operation) *Operation {
	if o == nil {
		return nil
	}
	c := *o
	c.Result = append(json.RawMessage(nil), o.Result...)
	return &c
}

// cloneManifest returns a manifest that shares no memory with m, by writing it
// out and parsing it again: a manifest has slices, pointers and raw JSON at
// several depths, and going through its own parser keeps the copy complete
// whatever fields the manifest package adds. m came from manifest.Parse, so a
// failure here is a defect and not an input.
func cloneManifest(m manifest.Manifest) manifest.Manifest {
	data, err := json.Marshal(m)
	if err != nil {
		panic("config: a parsed manifest cannot be written out: " + err.Error())
	}
	c, err := manifest.Parse(data)
	if err != nil {
		panic("config: a parsed manifest is refused when parsed again: " + err.Error())
	}
	return c
}
