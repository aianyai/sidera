//go:build testclock || crashtest

package manageapi

import (
	"context"
	"encoding/json"
)

// testMethods are the test rows of a binary built with a test tag: the door of the
// test harness to the node it runs in. The methods of the harness (the test clock,
// quiescence, the crash points) are added here by the streams that own them, each
// in a file of its own tag; the one row below is only there to show the shape and
// that a tagged build has it.
func testMethods() []*method { return []*method{testPing} }

// test.ping answers an empty object with {"pong": true}: a row that proves the
// table has its test rows, and takes no params.
var testPing = &method{
	name: "test.ping",
	test: func(_ context.Context, raw json.RawMessage) (any, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return struct {
			Pong bool `json:"pong"`
		}{true}, nil
	},
}
