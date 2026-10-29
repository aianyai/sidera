//go:build crashtest

package manageapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/internal/node/txn"
)

// TestConfig is present only in crash-test builds. The composition root supplies
// the complete validated transaction table; the interface does not own it.
type TestConfig struct{ CrashPoints []txn.Key }

func crashMethods() []*method { return []*method{testCrashPoints} }

var testCrashPoints = &method{name: "test.crashpoints", test: func(s *Server, _ context.Context, raw json.RawMessage) (any, error) {
	var p struct{}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if s.cfg.CrashPoints == nil {
		return nil, fmt.Errorf("manageapi: no crash point table")
	}
	type point struct {
		Key  string `json:"key"`
		Kind string `json:"kind"`
	}
	result := struct {
		Points []point `json:"points"`
	}{Points: []point{}}
	for _, k := range s.cfg.CrashPoints {
		kind := "normal"
		if k.Kind == txn.Recovery {
			kind = "recovery"
		}
		result.Points = append(result.Points, point{Key: k.Key.String(), Kind: kind})
	}
	return result, nil
}}
