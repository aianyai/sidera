package engine_test

import (
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

// The helpers of the tests of feedback. What the node tells an endpoint is read
// from the disk, as the notices of the system endpoint that the endpoint holds, so
// that a test judges what was told and not how the engine remembers it.

// notice is a notice of the system endpoint to an endpoint of the node, with the
// words it says.
type notice struct {
	in   store.InRecord
	text string
}

// kinds are the kinds of feedback the notice stands for.
func (n notice) kinds() []delivery.Kind {
	var out []delivery.Kind
	for _, c := range n.in.Covers {
		out = append(out, c.Kind)
	}
	return out
}

// noticesFor are the notices the node has written for the endpoint registered as
// who, oldest first.
func noticesFor(t testing.TB, h *harness.H, who string) []notice {
	t.Helper()
	to := endpointOf(t, h, who)
	var out []notice
	for _, in := range scanOf(t, h).In {
		if !in.Key.Sender.IsSystem() || in.Key.Recipient != to {
			continue
		}
		n := notice{in: in}
		if in.Body != nil {
			b, err := h.Disk().ReadFile(path.Join(store.BodiesDir, store.BodyFileName(in.Name())))
			if err != nil {
				t.Fatalf("the body of notice %s: %v", in.Key.Message, err)
			}
			n.text = string(b)
		}
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b notice) int {
		if c := a.in.AcceptedAt.Compare(b.in.AcceptedAt); c != 0 {
			return c
		}
		return strings.Compare(string(a.in.Key.Message), string(b.in.Key.Message))
	})
	return out
}

// toldKinds are the kinds of feedback the endpoint was told, one entry for each
// item of each notice, in the order of the notices.
func toldKinds(t testing.TB, h *harness.H, who string) []delivery.Kind {
	t.Helper()
	var out []delivery.Kind
	for _, n := range noticesFor(t, h, who) {
		out = append(out, n.kinds()...)
	}
	return out
}

// pending is how many feedback items the source records of the endpoint hold that no
// notice has covered yet, and the out-lost records' items with them.
func pending(t testing.TB, h *harness.H, who string) int {
	t.Helper()
	sender := endpointOf(t, h, who)
	n := 0
	sc := scanOf(t, h)
	for _, o := range sc.Out {
		if o.Key.Sender == sender {
			n += len(o.Feedback)
		}
	}
	for _, o := range sc.OutLost {
		if o.Key.Sender == sender {
			n += len(o.Feedback)
		}
	}
	return n
}

// declineAll makes the endpoint refuse every sender, so that what is sent to it is
// rejected at once and the sender is owed one notice for it.
func declineAll(t testing.TB, h *harness.H, who string) {
	t.Helper()
	if err := h.Agent(who).Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &[]string{}}); err != nil {
		t.Fatal(err)
	}
}

// writeOnDisk writes a file of state/ on the disk of the node, which must be down.
func writeOnDisk(t testing.TB, h *harness.H, dir, name string, data []byte) {
	t.Helper()
	if err := fsx.Replace(h.Disk(), fsx.Tag{Tx: "offline", Role: "f"}, path.Join(dir, name), data, fsx.ModePrivate); err != nil {
		t.Fatalf("writing %s/%s: %v", dir, name, err)
	}
}

// damageBody changes one byte of the body of the record file, after its first line,
// so that the file still says what it is and no longer checks out. The node must be
// down.
func damageBody(t testing.TB, h *harness.H, dir, name string) {
	t.Helper()
	p := path.Join(dir, name)
	data, err := h.Disk().ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	nl := strings.IndexByte(string(data), '\n')
	if nl < 0 || nl+1 >= len(data) {
		t.Fatalf("%s has no body to damage", p)
	}
	data[len(data)-1] ^= 0x01
	writeOnDisk(t, h, dir, name, data)
}

// zeroFile makes the record file all zeros of the same length, as a power loss can
// leave a file whose blocks never reached the disk. The node must be down.
func zeroFile(t testing.TB, h *harness.H, dir, name string) {
	t.Helper()
	p := path.Join(dir, name)
	data, err := h.Disk().ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	writeOnDisk(t, h, dir, name, make([]byte, len(data)))
}

// alertsOfKind are the alerts the node holds of the kind.
func alertsOfKind(t testing.TB, h *harness.H, kind string) []store.AlertRecord {
	t.Helper()
	var out []store.AlertRecord
	for _, a := range scanOf(t, h).Alerts {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// restartedAt starts a node on a disk a crash left, with the clock where it stood at
// the instant of the crash, so that what a restart finds due is what was due then.
func restartedAt(t *testing.T, disk *fsxtest.Mem, at time.Time, names ...string) *harness.H {
	t.Helper()
	h := harness.New(t, harness.Options{Disk: disk, Start: at})
	for _, n := range names {
		h.MustDirect(n)
	}
	quiet(t, h)
	for i := 0; i < 3; i++ {
		h.Advance(pullInterval + mergeWindow)
		quiet(t, h)
	}
	return h
}
