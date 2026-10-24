package envelope

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestDefaultLimitsValidate(t *testing.T) {
	if err := DefaultLimits().check(); err != nil {
		t.Fatalf("the defaults fail their own check: %v", err)
	}
}

func TestDefaultLimitsValues(t *testing.T) {
	l := DefaultLimits()
	rows := []struct {
		rule string
		got  int
		want int
	}{
		{"body limit is 64 KiB: a long excerpt or log fits one message, and larger content is passed on by the agent", l.MaxBody, 64 * 1024},
		{"recipient limit is 50: the figure the adapter protocol already applies to one message", l.MaxRecipients, adapterproto.MaxRecipients},
		{"recipient limit is 50 as decided: changing the adapter protocol bound changes the wire protocol and must show here", l.MaxRecipients, 50},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if r.got != r.want {
				t.Fatalf("got %d, want %d", r.got, r.want)
			}
		})
	}
}

// messageTo is the golden message addressed to n recipients on its target
// node, with one sequence number each, in the order the wire form requires.
func messageTo(t testing.TB, n int) envelopeWire {
	t.Helper()
	w := wireOf(t, baseMessage(t))
	node := w.To[0].Node
	w.MultiRecipient = n > 1
	w.To = w.To[:0]
	w.Seq = w.Seq[:0]
	for i := 1; i <= n; i++ {
		w.To = append(w.To, addressWire{Node: node, Endpoint: ep(byte(i)).Bytes()})
		w.Seq = append(w.Seq, seqWire{Endpoint: ep(byte(i)).Bytes(), Seq: 1})
	}
	return w
}

func TestDefaultLimitsBehaviour(t *testing.T) {
	lim := DefaultLimits()
	t.Run("a body of 60 KiB is accepted", func(t *testing.T) {
		e := baseMessage(t)
		e.Body = bytes.Repeat([]byte{'a'}, 60*1024)
		b, err := e.Encode(lim)
		requireNoErr(t, err)
		_, err = Decode(b, lim)
		requireNoErr(t, err)
	})
	t.Run("a body of exactly 64 KiB is accepted", func(t *testing.T) {
		e := baseMessage(t)
		e.Body = bytes.Repeat([]byte{'a'}, 64*1024)
		_, err := e.Encode(lim)
		requireNoErr(t, err)
	})
	t.Run("a body one byte over 64 KiB is refused as too large", func(t *testing.T) {
		e := baseMessage(t)
		e.Body = bytes.Repeat([]byte{'a'}, 64*1024+1)
		_, err := e.Encode(lim)
		requireErr(t, err, ErrBodyTooLarge)
	})
	t.Run("50 recipients with the largest body fit the input limit", func(t *testing.T) {
		w := messageTo(t, 50)
		w.Body = bytes.Repeat([]byte{'a'}, 64*1024)
		b, err := record.MarshalCanonical(w)
		requireNoErr(t, err)
		if len(b) > lim.inputLimit() {
			t.Fatalf("encoded %d bytes, input limit %d", len(b), lim.inputLimit())
		}
		_, err = Decode(b, lim)
		requireNoErr(t, err)
	})
	t.Run("51 recipients are refused", func(t *testing.T) {
		requireErr(t, decodeWire(t, messageTo(t, 51), lim), ErrRecipients)
	})
}
