package store

import (
	"errors"
	"fmt"

	"github.com/aianyai/sidera/internal/ident"
)

// outLostJSON is the body of an out-lost record: the time the damage was found
// and, until a notice covers it, the sender of its one feedback item. Everything
// else about the item (its kind, that it names no message and no recipient, the
// time it was decided at, the record that holds it) is what the delivery package
// builds for that sender and time, so it is not written.
type outLostJSON struct {
	At       *wtime        `json:"at"`
	Feedback []outLostItem `json:"feedback,omitempty"`
}

type outLostItem struct {
	Sender string `json:"sender"`
}

func encodeOutLost(r OutLostRecord) ([]byte, error) {
	if r.Key.Sender.IsZero() {
		return nil, errors.New("an out-lost record without a sending endpoint")
	}
	if !isHashName(r.Key.Damaged) {
		return nil, errors.New("an out-lost record without the hash of its damaged file")
	}
	at, err := wreq(r.DiscoveredAt, "the discovery time")
	if err != nil {
		return nil, err
	}
	j := outLostJSON{At: &at}
	switch len(r.Feedback) {
	case 0:
	case 1:
		it := r.Feedback[0]
		want := newOutLostItem(it.Ref.Sender, r.Name(), r.DiscoveredAt)
		if it.Ref.Sender.Endpoint != r.Key.Sender || it.Ref != want.Ref || it.Feedback != want.Feedback || it.Source != want.Source || !it.At.Equal(want.At) {
			return nil, errors.New("the feedback item of an out-lost record is the one of its sender, its file and its discovery time")
		}
		j.Feedback = []outLostItem{{Sender: it.Ref.Sender.String()}}
	default:
		return nil, fmt.Errorf("an out-lost record holds one feedback item, not %d", len(r.Feedback))
	}
	return marshal(j)
}

func decodeOutLost(key OutLostKey, body []byte) (OutLostRecord, error) {
	var j outLostJSON
	if err := unmarshal(body, &j); err != nil {
		return OutLostRecord{}, err
	}
	at, err := tmust(j.At, "the discovery time")
	if err != nil {
		return OutLostRecord{}, err
	}
	r := OutLostRecord{Key: key, DiscoveredAt: at}
	if len(j.Feedback) > 1 {
		return r, fmt.Errorf("an out-lost record holds one feedback item, not %d", len(j.Feedback))
	}
	for _, it := range j.Feedback {
		sender, err := ident.ParseAddress(it.Sender)
		if err != nil {
			return r, err
		}
		if sender.Endpoint != key.Sender {
			return r, errors.New("the feedback item of an out-lost record is for the endpoint of its key")
		}
		r.Feedback = append(r.Feedback, newOutLostItem(sender, r.Name(), at))
	}
	return r, nil
}
