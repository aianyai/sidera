package config

import (
	"cmp"
	"encoding/json"
	"slices"

	"github.com/aianyai/sidera/internal/ident"
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
