package protocol_test

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func address(t testing.TB, fill byte) ident.Address {
	t.Helper()
	node := make([]byte, ident.NodeIDLen)
	ep := make([]byte, ident.EndpointIDLen)
	for i := range node {
		node[i] = fill + byte(i)
	}
	for i := range ep {
		ep[i] = fill + byte(2*i) + 1
	}
	n, err := ident.NodeIDFromBytes(node)
	if err != nil {
		t.Fatal(err)
	}
	e, err := ident.EndpointIDFromBytes(ep)
	if err != nil {
		t.Fatal(err)
	}
	return ident.Address{Node: n, Endpoint: e}
}

// TestRecvPageFromHandoff: whatever handoff.Pack and handoff.Part print for a
// recv is a RecvPage, to the schema and to the Go type.
func TestRecvPageFromHandoff(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	newLease := func() delivery.LeaseID {
		id, err := delivery.NewLeaseID(rng)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	must := func(p handoff.Page, err error) handoff.Page {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	from := address(t, 3)
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 80)
	msgs := map[string]handoff.Message{
		"plain":        {ID: "m-1", From: from, Body: "Please run the migration."},
		"duplicate":    {ID: "m-2", From: from, Body: "again", PossiblyDuplicate: true},
		"markup":       {ID: "m-3", From: from, Body: "a < b && \"c\" > d\nsecond line\ttab   and   and é 変更 \U0001F600"},
		"long":         {ID: "m-long", From: from, Body: long},
		"long resumed": {ID: "m-long2", From: from, Body: strings.Repeat(long, 8), NextPart: 2},
		"long dup":     {ID: "m-long3", From: from, Body: long, PossiblyDuplicate: true},
	}
	budgets := []handoff.Budget{
		handoff.DefaultBudget(),
		{Unit: handoff.UnitChars, Limit: 600},
		{Unit: handoff.UnitTokens, Limit: 1500},
		{Unit: handoff.UnitChars, Limit: 131072},
	}
	type seen struct{ empty, whole, duplicate, parts, next, lastPart, more int }
	var cov seen
	check := func(name string, page handoff.Page) {
		t.Helper()
		// The schema accepts the printed document.
		doc, err := parse(page.Output)
		if err != nil {
			t.Fatalf("%s: output is not JSON: %v", name, err)
		}
		if err := schemas(t).accepts(t, "types.schema.json#/$defs/RecvPage", doc); err != nil {
			t.Fatalf("%s: the schema rejects what recv printed: %v\n%s", name, err, page.Output)
		}
		// The Go type decodes it strictly and encodes it back to the same data.
		var rp adapterproto.RecvPage
		if err := adapterproto.Decode(page.Output, &rp); err != nil {
			t.Fatalf("%s: RecvPage rejects what recv printed: %v\n%s", name, err, page.Output)
		}
		again, err := adapterproto.Encode(rp)
		if err != nil {
			t.Fatalf("%s: encode: %v", name, err)
		}
		if same, err := equalJSON(page.Output, again); err != nil || !same {
			t.Fatalf("%s: round trip changed the page\n was %s\n now %s", name, page.Output, again)
		}
		// The page is what was handed out.
		if len(rp.Messages) != len(page.HandedOut) {
			t.Fatalf("%s: %d messages on the page, %d handed out", name, len(rp.Messages), len(page.HandedOut))
		}
		if rp.More != page.Remaining {
			t.Fatalf("%s: more is %d, remaining %d", name, rp.More, page.Remaining)
		}
		switch {
		case len(rp.Messages) == 0:
			cov.empty++
		default:
			for _, m := range rp.Messages {
				if m.PossiblyDuplicate {
					cov.duplicate++
				}
				if m.Part == nil {
					cov.whole++
					continue
				}
				cov.parts++
				if m.Part.Next != nil {
					cov.next++
				} else {
					cov.lastPart++
				}
			}
		}
		if rp.More > 0 {
			cov.more++
		}
	}
	for bi, b := range budgets {
		check("empty queue", must(handoff.Pack(nil, newLease(), b)))
		for name, m := range msgs {
			if name == "long resumed" && b.Limit >= 100000 {
				continue // the message fits whole at this budget, so it has no part 2
			}
			label := name + "/" + string(rune('a'+bi))
			check("Pack "+label, must(handoff.Pack([]handoff.Message{m}, newLease(), b)))
			n := 1
			if strings.HasPrefix(name, "long") {
				n = 12
			}
			for k := 1; k <= n; k++ {
				p, err := handoff.Part(m, k, newLease(), b)
				if err != nil {
					break // no such part: the message has fewer parts at this budget
				}
				check("Part "+label, p)
			}
		}
		queue := []handoff.Message{msgs["plain"], msgs["duplicate"], msgs["markup"]}
		check("queue", must(handoff.Pack(queue, newLease(), b)))
		check("queue with a long message", must(handoff.Pack([]handoff.Message{msgs["plain"], msgs["long"]}, newLease(), b)))
		check("long message first", must(handoff.Pack([]handoff.Message{msgs["long"], msgs["plain"]}, newLease(), b)))
	}
	t.Logf("pages seen: %+v", cov)
	if cov.empty == 0 || cov.whole == 0 || cov.duplicate == 0 || cov.parts == 0 || cov.next == 0 || cov.lastPart == 0 || cov.more == 0 {
		t.Errorf("the pages did not cover every shape: %+v", cov)
	}
}
