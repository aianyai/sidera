package adapterproto

import (
	"encoding/json"
	"strings"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// Message is a parameter or result type of the protocol.
type Message interface {
	Validate() error
}

// Decode strictly decodes data, one JSON value, into v and validates it. v is a
// pointer to a message type. Strict means: unknown fields, fields whose name
// differs in case, null values, missing required fields and explicit empty
// values for optional fields are all errors. A message that Decode accepts is
// one the schema accepts.
func Decode(data []byte, v Message) error {
	if err := strictjson.Decode(data, v); err != nil {
		return &ValidationError{Msg: strings.TrimPrefix(err.Error(), "strictjson: ")}
	}
	return asValidation(v.Validate())
}

// Encode validates v and encodes it as JSON. A message that fails validation is
// never written.
func Encode(v Message) ([]byte, error) {
	if err := asValidation(v.Validate()); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
