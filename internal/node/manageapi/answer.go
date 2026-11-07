package manageapi

import (
	"encoding/json"

	"github.com/aianyai/sidera/internal/node/apiwire"
)

// Answer is the envelope of an answer of success that Client.Call returned.
// OperationID is set for the answer to a change and empty for the answer to a
// read, so it also tells a caller which of the two it holds.
type Answer struct {
	OperationID string          `json:"operation_id"`
	Replayed    bool            `json:"replayed"`
	Result      json.RawMessage `json:"result"`
}

// ReadAnswer reads the envelope of an answer of success, ignoring members it does
// not know. Client.Call has already checked the envelope it returns, so the error
// is for bytes that came from elsewhere, and it is not the strictness of a
// request: encoding/json reads the envelope, as it reads the result. The node said the
// request succeeded, so the error is internal/answer_unreadable and not an unknown
// outcome; with no readable envelope there is no identifier and no way to tell a
// change from a read, so it says nothing of a change.
func ReadAnswer(raw []byte) (Answer, error) {
	var a Answer
	if err := json.Unmarshal(raw, &a); err != nil {
		return Answer{}, apiwire.AnswerUnreadable(false, "")
	}
	return a, nil
}

// DecodeResult reads the result of the answer into v. It is the one place where a
// program reads a method's result. It follows encoding/json, not the strict
// decoding of requests: a result member that v does not name is ignored, a member
// that v names with another type is an error, and so is a result that is absent.
// The error is internal/answer_unreadable (see Unreadable).
func (a Answer) DecodeResult(v any) error {
	if err := json.Unmarshal(a.Result, v); err != nil {
		return a.Unreadable()
	}
	return nil
}

// Unreadable is what a program says when the node answered that the request
// succeeded and the program cannot make sense of the answer, most likely because
// the node is newer. A change that the node accepted is not unknown: the error
// names its operation identifier and says it was accepted, so that nothing is sent
// again.
func (a Answer) Unreadable() error {
	return apiwire.AnswerUnreadable(a.OperationID != "", a.OperationID)
}

// DecodeResult reads the envelope of raw and then its result into v, for a caller
// that needs nothing else of the answer.
func DecodeResult(raw []byte, v any) error {
	a, err := ReadAnswer(raw)
	if err != nil {
		return err
	}
	return a.DecodeResult(v)
}
