package crashwalk_test

import (
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

// A declaration that is listed as not yet walked and was not reached is owed and not
// missing; one that is listed and was reached is an error, so the list shrinks as
// recipes are written; one that is not listed and was not reached is missing.
func TestAssessSetsAsideTheDeclarationsNotYetWalked(t *testing.T) {
	tb := table(t)
	hitsWithout := func(tx string) []crashwalk.Hit {
		var out []crashwalk.Hit
		for _, k := range tb.Keys() {
			if k.Tag.Tx != tx {
				out = append(out, crashwalk.Hit{Gen: 1, Point: fsx.Point{Key: k.Key, Hit: 1}})
			}
		}
		return out
	}
	owed := map[string]string{"status": "no recipe yet"}
	for _, c := range []struct {
		name                     string
		notYetWalked             map[string]string
		hits                     []crashwalk.Hit
		deferred, missing, stale bool
		staleMentions            string
	}{
		{"listed and not reached", owed, hitsWithout("status"), true, false, false, ""},
		{"listed and reached", owed, hitsWithout(""), false, false, true, "status"},
		{"not listed and not reached", nil, hitsWithout("status"), false, true, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := crashwalk.AssessWith(tb, c.hits, c.notYetWalked)
			if (len(got.Deferred) > 0) != c.deferred || (len(got.MissingNormal) > 0) != c.missing || (len(got.StaleDeferred) > 0) != c.stale {
				t.Fatalf("deferred %v, missing %v, stale %v; want %v, %v, %v", got.Deferred, got.MissingNormal, got.StaleDeferred, c.deferred, c.missing, c.stale)
			}
			if c.stale && !strings.Contains(got.StaleDeferred[0], c.staleMentions) {
				t.Errorf("the stale entry says %q, want it to say %q", got.StaleDeferred[0], c.staleMentions)
			}
		})
	}
}

// Every entry of the list says why it is there.
func TestEveryDeclarationNotYetWalkedSaysWhy(t *testing.T) {
	for name, why := range crashwalk.NotYetWalked {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%q has no reason", name)
		}
	}
}
