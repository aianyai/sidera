//go:build testclock || crashtest || netacceptance

package manageapi

import (
	"context"
	"encoding/json"
)

// testMethods combines only the rows enabled by their own build tags.
func testMethods() []*method {
	rows := append([]*method{testPing}, clockMethods()...)
	rows = append(rows, crashMethods()...)
	return append(rows, netMethods()...)
}

// test.ping answers an empty object with {"pong": true}: a row that proves the
// table has its test rows, and takes no params.
var testPing = &method{
	name: "test.ping",
	test: func(_ *Server, _ context.Context, raw json.RawMessage) (any, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return struct {
			Pong bool `json:"pong"`
		}{true}, nil
	},
}
