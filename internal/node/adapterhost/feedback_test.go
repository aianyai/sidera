package adapterhost_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A page reaches an adapter with the feedback member only when the adapter
// declared direct.feedback; to any other the host leaves the member out, so that
// an adapter that decodes a page strictly never meets a member it does not know.
// The rest of the page is the same either way.
func TestRecvCarriesFeedbackOnlyToAnAdapterThatDeclaredIt(t *testing.T) {
	for _, declared := range []bool{true, false} {
		t.Run(fmt.Sprintf("declared=%v", declared), func(t *testing.T) {
			r := newRig(t, rigOptions{})
			page := adapterproto.RecvPage{
				Lease: "0123456789abcdef0123456789abcdef",
				Messages: []adapterproto.PageMessage{
					{ID: "m-1", From: "node/system", Feedback: true, Body: "Notice from the channel of this node."},
					{ID: "m-2", From: "bob/chat", Body: "hello"},
				},
			}
			r.eng.recvPage = &page
			var a *scripted
			r.lau.launch = func(int, adapterhost.Instance) (adapterhost.Process, error) {
				pp, s := r.scriptedProcess(func(s *scripted) {
					caps := r.opts.Capabilities()
					caps.Direct.Feedback = declared
					s.caps = &caps
				}, false)
				a = s
				return pp, nil
			}
			r.start()
			r.ready("chat", 1)

			var raw json.RawMessage
			if err := a.call(adapterproto.MethodEndpointRecv, adapterproto.RecvParams{Endpoint: "ep-1"}, &raw); err != nil {
				t.Fatal(err)
			}
			var got adapterproto.RecvResult
			if err := adapterproto.Decode(raw, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Page.Messages) != 2 || got.Page.Messages[0].Body != page.Messages[0].Body || got.Page.Messages[1].Body != "hello" {
				t.Fatalf("the page changed beyond the member: %+v", got.Page)
			}
			if has := strings.Contains(string(raw), `"feedback"`); has != declared {
				t.Errorf("the page carries the member = %v, want %v: %s", has, declared, raw)
			}
			if !page.Messages[0].Feedback {
				t.Error("the engine's page was changed in place")
			}
		})
	}
}
