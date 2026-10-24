package adapterproto_test

import (
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func page() adapterproto.RecvPage {
	return adapterproto.RecvPage{Lease: lease1, More: 2, Messages: []adapterproto.PageMessage{
		{ID: "m-1", From: addr1, Body: "hello"},
		{ID: "m-2", From: addr1, PossiblyDuplicate: true, Body: ""},
		{ID: "m-big", From: addr1, Body: "part", Part: &adapterproto.PagePart{Index: 1, Total: 3, Label: "part 1/3", Next: &adapterproto.PageNext{Msg: "m-big", Part: 2}}},
	}}
}

func TestRecvPageRules(t *testing.T) {
	method(t, page, []row[adapterproto.RecvPage]{
		{"lease required", func(p *adapterproto.RecvPage) { p.Lease = "" }, "lease", false},
		{"lease uppercase", func(p *adapterproto.RecvPage) { p.Lease = "0123456789ABCDEF0123456789ABCDEF" }, "lease", false},
		{"lease of 31 characters", func(p *adapterproto.RecvPage) { p.Lease = adapterproto.LeaseRef(lease1[:31]) }, "lease", false},
		{"lease of 33 characters", func(p *adapterproto.RecvPage) { p.Lease = adapterproto.LeaseRef(lease1 + "0") }, "lease", false},
		{"lease not hex", func(p *adapterproto.RecvPage) { p.Lease = "g123456789abcdef0123456789abcdef" }, "lease", false},
		{"the all-zero lease names no lease", func(p *adapterproto.RecvPage) { p.Lease = "00000000000000000000000000000000" }, "lease", false},
		{"lease with trailing newline", func(p *adapterproto.RecvPage) { p.Lease = adapterproto.LeaseRef(lease1[:31] + "\n") }, "lease", false},
		{"messages required", func(p *adapterproto.RecvPage) { p.Messages = nil }, "messages", false},
		{"an empty page", func(p *adapterproto.RecvPage) { p.Messages = []adapterproto.PageMessage{}; p.More = 0 }, "", false},
		{"more required to be non-negative", func(p *adapterproto.RecvPage) { p.More = -1 }, "more", false},
		{"more at the bound", func(p *adapterproto.RecvPage) { p.More = 1<<31 - 1 }, "", false},
		{"more over the bound", func(p *adapterproto.RecvPage) { p.More = 1 << 31 }, "more", false},
		{"message id required", func(p *adapterproto.RecvPage) { p.Messages[0].ID = "" }, "messages[0].id", false},
		{"message sender required", func(p *adapterproto.RecvPage) { p.Messages[0].From = "" }, "messages[0].from", false},
		{"message sender over the limit", func(p *adapterproto.RecvPage) { p.Messages[0].From = adapterproto.Address(repeat("a", 129)) }, "messages[0].from", false},
		{"message body may be empty", func(p *adapterproto.RecvPage) { p.Messages[0].Body = "" }, "", false},
		{"part index zero", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Index = 0 }, "messages[2].part.index", false},
		{"part total zero", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Total = 0 }, "messages[2].part.total", false},
		{"part label not of the form part i/n", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Label = "first" }, "messages[2].part.label", false},
		{"part label disagrees with the numbers", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Label = "part 2/3" }, "messages[2].part.label", true},
		{"part index beyond the total", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Index = 4; p.Messages[2].Part.Label = "part 4/3" }, "messages[2].part.index", true},
		{"the last part has no next", func(p *adapterproto.RecvPage) {
			pt := p.Messages[2].Part
			pt.Index, pt.Label, pt.Next = 3, "part 3/3", nil
		}, "", false},
		{"the last part with a next", func(p *adapterproto.RecvPage) {
			pt := p.Messages[2].Part
			pt.Index, pt.Label = 3, "part 3/3"
			pt.Next = &adapterproto.PageNext{Msg: "m-big", Part: 4}
		}, "messages[2].part.next", true},
		{"a middle part without next", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Next = nil }, "messages[2].part.next", true},
		{"next names another message", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Next.Msg = "other" }, "messages[2].part.next.msg", true},
		{"next skips a part", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Next.Part = 3 }, "messages[2].part.next", true},
		{"next part zero", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Next.Part = 0 }, "messages[2].part.next.part", false},
		{"next message required", func(p *adapterproto.RecvPage) { p.Messages[2].Part.Next.Msg = "" }, "messages[2].part.next.msg", false},
	})
}

func TestRecvObservedRules(t *testing.T) {
	method(t, func() adapterproto.RecvObservedParams {
		return adapterproto.RecvObservedParams{Endpoint: ep1, Lease: lease1, Frontend: "cli", Observed: "{}\n"}
	}, []row[adapterproto.RecvObservedParams]{
		{"endpoint required", func(p *adapterproto.RecvObservedParams) { p.Endpoint = "" }, "endpoint", false},
		{"lease validated", func(p *adapterproto.RecvObservedParams) { p.Lease = "abc" }, "lease", false},
		{"front end mcp", func(p *adapterproto.RecvObservedParams) { p.Frontend = "mcp" }, "", false},
		{"front end direct has no tool result", func(p *adapterproto.RecvObservedParams) { p.Frontend = "direct" }, "frontend", false},
		{"front end required", func(p *adapterproto.RecvObservedParams) { p.Frontend = "" }, "frontend", false},
		{"observed may be empty", func(p *adapterproto.RecvObservedParams) { p.Observed = "" }, "", false},
		{"observed may be large", func(p *adapterproto.RecvObservedParams) { p.Observed = repeat("x", 200000) }, "", false},
	})
	allowed := func() adapterproto.RecvObservedResult {
		return adapterproto.RecvObservedResult{Verdict: "allowed", Reason: "seen_in_full", Items: []adapterproto.PartAck{{MessageID: "m-1", Part: 1, Ack: "completed"}}}
	}
	t.Run("allowed", func(t *testing.T) {
		method(t, allowed, []row[adapterproto.RecvObservedResult]{
			{"every item ack", func(r *adapterproto.RecvObservedResult) {
				r.Items = nil
				for _, a := range []adapterproto.ItemAck{"recorded", "completed", "duplicate", "refused_no_handout", "refused_lease", "too_late"} {
					r.Items = append(r.Items, adapterproto.PartAck{MessageID: "m", Part: len(r.Items) + 1, Ack: a})
				}
			}, "", false},
			{"reason must be seen_in_full", func(r *adapterproto.RecvObservedResult) { r.Reason = "replaced" }, "reason", false},
			{"items required", func(r *adapterproto.RecvObservedResult) { r.Items = nil }, "items", false},
			{"at least one item", func(r *adapterproto.RecvObservedResult) { r.Items = []adapterproto.PartAck{} }, "items", false},
			{"item ack unknown", func(r *adapterproto.RecvObservedResult) { r.Items[0].Ack = "refused_token" }, "items[0].ack", false},
			{"item part zero", func(r *adapterproto.RecvObservedResult) { r.Items[0].Part = 0 }, "items[0].part", false},
			{"item message id required", func(r *adapterproto.RecvObservedResult) { r.Items[0].MessageID = "" }, "items[0].message_id", false},
			{"1024 items", func(r *adapterproto.RecvObservedResult) { r.Items = manyAcks(1024) }, "", false},
			{"1025 items", func(r *adapterproto.RecvObservedResult) { r.Items = manyAcks(1025) }, "items", false},
		})
	})
	t.Run("refused", func(t *testing.T) {
		method(t, func() adapterproto.RecvObservedResult {
			return adapterproto.RecvObservedResult{Verdict: "refused", Reason: "replaced", Items: []adapterproto.PartAck{}}
		}, []row[adapterproto.RecvObservedResult]{
			{"truncated", func(r *adapterproto.RecvObservedResult) { r.Reason = "truncated" }, "", false},
			{"content mismatch", func(r *adapterproto.RecvObservedResult) { r.Reason = "content_mismatch" }, "", false},
			{"unknown lease", func(r *adapterproto.RecvObservedResult) { r.Reason = "unknown_lease" }, "", false},
			{"seen in full is not a refusal", func(r *adapterproto.RecvObservedResult) { r.Reason = "seen_in_full" }, "reason", false},
			{"reason required", func(r *adapterproto.RecvObservedResult) { r.Reason = "" }, "reason", false},
			{"items must be empty", func(r *adapterproto.RecvObservedResult) { r.Items = allowed().Items }, "items", false},
			{"items required", func(r *adapterproto.RecvObservedResult) { r.Items = nil }, "items", false},
			{"verdict required", func(r *adapterproto.RecvObservedResult) { r.Verdict = "" }, "verdict", false},
			{"verdict unknown", func(r *adapterproto.RecvObservedResult) { r.Verdict = "later" }, "verdict", false},
		})
	})
}

func manyAcks(n int) []adapterproto.PartAck {
	out := make([]adapterproto.PartAck, n)
	for i := range out {
		out[i] = adapterproto.PartAck{MessageID: "m", Part: i + 1, Ack: "recorded"}
	}
	return out
}

func TestDirectEndpointRules(t *testing.T) {
	method(t, func() adapterproto.RegisterParams {
		return adapterproto.RegisterParams{Key: "chat:alice", DisplayName: "Alice", Description: "Alice, through the bridge"}
	}, []row[adapterproto.RegisterParams]{
		{"key required", func(p *adapterproto.RegisterParams) { p.Key = "" }, "key", false},
		{"key at the limit", func(p *adapterproto.RegisterParams) { p.Key = repeat("k", 128) }, "", false},
		{"key over the limit", func(p *adapterproto.RegisterParams) { p.Key = repeat("k", 129) }, "key", false},
		{"display name required", func(p *adapterproto.RegisterParams) { p.DisplayName = "" }, "display_name", false},
		{"display name over the limit", func(p *adapterproto.RegisterParams) { p.DisplayName = repeat("n", 129) }, "display_name", false},
		{"description optional", func(p *adapterproto.RegisterParams) { p.Description = "" }, "", false},
		{"description at the limit", func(p *adapterproto.RegisterParams) { p.Description = repeat("d", 1024) }, "", false},
		{"description over the limit", func(p *adapterproto.RegisterParams) { p.Description = repeat("d", 1025) }, "description", false},
	})
	method(t, func() adapterproto.PendingParams { return adapterproto.PendingParams{Endpoint: ep1, Count: 3} },
		[]row[adapterproto.PendingParams]{
			{"count one", func(p *adapterproto.PendingParams) { p.Count = 1 }, "", false},
			{"count zero", func(p *adapterproto.PendingParams) { p.Count = 0 }, "count", false},
			{"count negative", func(p *adapterproto.PendingParams) { p.Count = -1 }, "count", false},
			{"endpoint required", func(p *adapterproto.PendingParams) { p.Endpoint = "" }, "endpoint", false},
		})
}

func ptr[T any](v T) *T { return &v }

func TestEndpointRecvRules(t *testing.T) {
	method(t, func() adapterproto.RecvParams { return adapterproto.RecvParams{Endpoint: ep1, WaitS: ptr(10)} },
		[]row[adapterproto.RecvParams]{
			{"no wait given", func(p *adapterproto.RecvParams) { p.WaitS = nil }, "", false},
			{"wait of zero is allowed", func(p *adapterproto.RecvParams) { p.WaitS = ptr(0) }, "", false},
			{"wait of 30", func(p *adapterproto.RecvParams) { p.WaitS = ptr(30) }, "", false},
			{"wait of 31", func(p *adapterproto.RecvParams) { p.WaitS = ptr(31) }, "wait_s", false},
			{"wait negative", func(p *adapterproto.RecvParams) { p.WaitS = ptr(-1) }, "wait_s", false},
			{"a specific part", func(p *adapterproto.RecvParams) { p.MessageID = "m-big"; p.Part = 2 }, "", false},
			{"message without part", func(p *adapterproto.RecvParams) { p.MessageID = "m-big" }, "message_id and part", false},
			{"part without message", func(p *adapterproto.RecvParams) { p.Part = 2 }, "message_id and part", false},
			{"part negative", func(p *adapterproto.RecvParams) { p.MessageID = "m"; p.Part = -1 }, "part", false},
			{"part over the bound", func(p *adapterproto.RecvParams) { p.MessageID = "m"; p.Part = 1000001 }, "part", false},
			{"endpoint required", func(p *adapterproto.RecvParams) { p.Endpoint = "" }, "endpoint", false},
		})
	method(t, func() adapterproto.RecvResult { return adapterproto.RecvResult{Page: page()} },
		[]row[adapterproto.RecvResult]{
			{"page validated", func(r *adapterproto.RecvResult) { r.Page.Lease = "x" }, "page.lease", false},
		})
}

func TestEndpointAckRules(t *testing.T) {
	method(t, func() adapterproto.AckParams {
		return adapterproto.AckParams{Endpoint: ep1, Lease: lease1, Items: []adapterproto.PartRef{{MessageID: "m-1", Part: 1}}}
	}, []row[adapterproto.AckParams]{
		{"endpoint required", func(p *adapterproto.AckParams) { p.Endpoint = "" }, "endpoint", false},
		{"lease validated", func(p *adapterproto.AckParams) { p.Lease = "zz" }, "lease", false},
		{"items required", func(p *adapterproto.AckParams) { p.Items = nil }, "items", false},
		{"items empty", func(p *adapterproto.AckParams) { p.Items = []adapterproto.PartRef{} }, "items", false},
		{"1024 items", func(p *adapterproto.AckParams) { p.Items = manyRefs(1024) }, "", false},
		{"1025 items", func(p *adapterproto.AckParams) { p.Items = manyRefs(1025) }, "items", false},
		{"item message required", func(p *adapterproto.AckParams) { p.Items[0].MessageID = "" }, "items[0].message_id", false},
		{"item part zero", func(p *adapterproto.AckParams) { p.Items[0].Part = 0 }, "items[0].part", false},
	})
	method(t, func() adapterproto.AckResult { return adapterproto.AckResult{Items: manyAcks(2)} },
		[]row[adapterproto.AckResult]{
			{"items required", func(r *adapterproto.AckResult) { r.Items = nil }, "items", false},
			{"items empty", func(r *adapterproto.AckResult) { r.Items = []adapterproto.PartAck{} }, "items", false},
			{"item ack unknown", func(r *adapterproto.AckResult) { r.Items[1].Ack = "maybe" }, "items[1].ack", false},
		})
}

func manyRefs(n int) []adapterproto.PartRef {
	out := make([]adapterproto.PartRef, n)
	for i := range out {
		out[i] = adapterproto.PartRef{MessageID: "m", Part: i + 1}
	}
	return out
}

func TestEndpointSendRules(t *testing.T) {
	method(t, func() adapterproto.SendParams {
		return adapterproto.SendParams{Endpoint: ep1, To: []adapterproto.Address{addr1}, ContentType: "text/plain", Body: "Ship it.",
			ReplyTo: "m-1", Thread: "build-117", DiscloseRecipients: true, AcceptBeforeS: 3600, IdempotencyKey: "evt-1"}
	}, []row[adapterproto.SendParams]{
		{"endpoint required", func(p *adapterproto.SendParams) { p.Endpoint = "" }, "endpoint", false},
		{"recipients required", func(p *adapterproto.SendParams) { p.To = nil }, "to", false},
		{"recipient list empty", func(p *adapterproto.SendParams) { p.To = []adapterproto.Address{} }, "to", false},
		{"50 recipients", func(p *adapterproto.SendParams) { p.To = manyAddrs(50) }, "", false},
		{"51 recipients", func(p *adapterproto.SendParams) { p.To = manyAddrs(51) }, "to", false},
		{"recipient empty", func(p *adapterproto.SendParams) { p.To = []adapterproto.Address{""} }, "to[0]", false},
		{"recipient over the limit", func(p *adapterproto.SendParams) {
			p.To = []adapterproto.Address{adapterproto.Address(repeat("a", 129))}
		}, "to[0]", false},
		{"content type required", func(p *adapterproto.SendParams) { p.ContentType = "" }, "content_type", false},
		{"content type over the limit", func(p *adapterproto.SendParams) { p.ContentType = repeat("c", 129) }, "content_type", false},
		{"body may be empty", func(p *adapterproto.SendParams) { p.Body = "" }, "", false},
		{"reply to optional", func(p *adapterproto.SendParams) { p.ReplyTo = "" }, "", false},
		{"reply to over the limit", func(p *adapterproto.SendParams) { p.ReplyTo = adapterproto.MessageID(repeat("m", 129)) }, "reply_to", false},
		{"thread optional", func(p *adapterproto.SendParams) { p.Thread = "" }, "", false},
		{"thread over the limit", func(p *adapterproto.SendParams) { p.Thread = repeat("t", 129) }, "thread", false},
		{"disclose is optional", func(p *adapterproto.SendParams) { p.DiscloseRecipients = false }, "", false},
		{"accept before optional", func(p *adapterproto.SendParams) { p.AcceptBeforeS = 0 }, "", false},
		{"accept before one second", func(p *adapterproto.SendParams) { p.AcceptBeforeS = 1 }, "", false},
		{"accept before at the bound", func(p *adapterproto.SendParams) { p.AcceptBeforeS = 1<<31 - 1 }, "", false},
		{"accept before over the bound", func(p *adapterproto.SendParams) { p.AcceptBeforeS = 1 << 31 }, "accept_before_s", false},
		{"accept before negative", func(p *adapterproto.SendParams) { p.AcceptBeforeS = -1 }, "accept_before_s", false},
		{"idempotency key optional", func(p *adapterproto.SendParams) { p.IdempotencyKey = "" }, "", false},
		{"idempotency key over the limit", func(p *adapterproto.SendParams) { p.IdempotencyKey = repeat("i", 129) }, "idempotency_key", false},
		{"control session.create with a preset", func(p *adapterproto.SendParams) {
			p.Control = &adapterproto.Control{Signal: "session.create", Params: &adapterproto.ControlParams{Preset: "default"}}
		}, "", false},
		{"control session.create without params", func(p *adapterproto.SendParams) { p.Control = &adapterproto.Control{Signal: "session.create"} }, "control.params", false},
		{"control session.create with a bad preset", func(p *adapterproto.SendParams) {
			p.Control = &adapterproto.Control{Signal: "session.create", Params: &adapterproto.ControlParams{Preset: "Bad Preset"}}
		}, "control.params.preset", false},
		{"control session.close", func(p *adapterproto.SendParams) { p.Control = &adapterproto.Control{Signal: "session.close"} }, "", false},
		{"control session.interrupt", func(p *adapterproto.SendParams) { p.Control = &adapterproto.Control{Signal: "session.interrupt"} }, "", false},
		{"control endpoint.status", func(p *adapterproto.SendParams) { p.Control = &adapterproto.Control{Signal: "endpoint.status"} }, "", false},
		{"control session.close with params", func(p *adapterproto.SendParams) {
			p.Control = &adapterproto.Control{Signal: "session.close", Params: &adapterproto.ControlParams{Preset: "default"}}
		}, "control.params", false},
		{"control endpoint.status with params", func(p *adapterproto.SendParams) {
			p.Control = &adapterproto.Control{Signal: "endpoint.status", Params: &adapterproto.ControlParams{Preset: "default"}}
		}, "control.params", false},
		{"control signal with two recipients", func(p *adapterproto.SendParams) {
			p.To = manyAddrs(2)
			p.Control = &adapterproto.Control{Signal: "session.close"}
		}, "to", false},
		{"control create with two recipients", func(p *adapterproto.SendParams) {
			p.To = manyAddrs(2)
			p.Control = &adapterproto.Control{Signal: "session.create", Params: &adapterproto.ControlParams{Preset: "default"}}
		}, "to", false},
		{"control signal with fifty recipients", func(p *adapterproto.SendParams) {
			p.To = manyAddrs(50)
			p.Control = &adapterproto.Control{Signal: "endpoint.status"}
		}, "to", false},
		{"control signal unknown", func(p *adapterproto.SendParams) { p.Control = &adapterproto.Control{Signal: "message.send"} }, "control.signal", false},
		{"control signal required", func(p *adapterproto.SendParams) { p.Control = &adapterproto.Control{} }, "control.signal", false},
	})
	method(t, func() adapterproto.SendResult { return adapterproto.SendResult{MessageID: "m-91c4"} },
		[]row[adapterproto.SendResult]{{"message id required", func(r *adapterproto.SendResult) { r.MessageID = "" }, "message_id", false}})
	method(t, func() adapterproto.MessageStatusParams {
		return adapterproto.MessageStatusParams{Endpoint: ep1, MessageID: "m-1"}
	},
		[]row[adapterproto.MessageStatusParams]{
			{"endpoint required", func(p *adapterproto.MessageStatusParams) { p.Endpoint = "" }, "endpoint", false},
			{"message id required", func(p *adapterproto.MessageStatusParams) { p.MessageID = "" }, "message_id", false},
		})
}

func manyAddrs(n int) []adapterproto.Address {
	out := make([]adapterproto.Address, n)
	for i := range out {
		out[i] = addr1
	}
	return out
}

func TestMessageStatusRules(t *testing.T) {
	method(t, func() adapterproto.MessageStatusResult {
		return adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{{To: addr1, State: "delivered"}}}
	}, []row[adapterproto.MessageStatusResult]{
		{"recipients required", func(r *adapterproto.MessageStatusResult) { r.Recipients = nil }, "recipients", false},
		{"recipients empty", func(r *adapterproto.MessageStatusResult) { r.Recipients = []adapterproto.RecipientStatus{} }, "recipients", false},
		{"every non-rejected state", func(r *adapterproto.MessageStatusResult) {
			r.Recipients = nil
			for _, s := range []adapterproto.DeliveryState{"queued", "accepted", "delivered", "expired_unconfirmed", "delivery_unknown", "closed_unconfirmed"} {
				r.Recipients = append(r.Recipients, adapterproto.RecipientStatus{To: addr1, State: s})
			}
		}, "", false},
		{"every rejection reason", func(r *adapterproto.MessageStatusResult) {
			r.Recipients = nil
			for _, why := range []adapterproto.RejectReason{"unauthorized", "endpoint_missing", "endpoint_closed", "endpoint_declined", "too_large", "expired", "backlog_expired", "second_check_failed", "malformed", "deadline_out_of_range"} {
				r.Recipients = append(r.Recipients, adapterproto.RecipientStatus{To: addr1, State: "rejected", Reason: why})
			}
		}, "", false},
		{"rejected needs a reason", func(r *adapterproto.MessageStatusResult) { r.Recipients[0].State = "rejected" }, "recipients[0].reason", false},
		{"a reason only with rejected", func(r *adapterproto.MessageStatusResult) { r.Recipients[0].Reason = "expired" }, "recipients[0].reason", false},
		{"rejection reason none is not a reason", func(r *adapterproto.MessageStatusResult) {
			r.Recipients[0].State, r.Recipients[0].Reason = "rejected", "none"
		}, "recipients[0].reason", false},
		{"state unknown", func(r *adapterproto.MessageStatusResult) { r.Recipients[0].State = "lost" }, "recipients[0].state", false},
		{"recipient required", func(r *adapterproto.MessageStatusResult) { r.Recipients[0].To = "" }, "recipients[0].to", false},
		{"50 recipients", func(r *adapterproto.MessageStatusResult) { r.Recipients = manyStatus(50) }, "", false},
		{"51 recipients", func(r *adapterproto.MessageStatusResult) { r.Recipients = manyStatus(51) }, "recipients", false},
		{"control reply validated", func(r *adapterproto.MessageStatusResult) {
			r.ControlReply = &adapterproto.ControlReply{Signal: "session.close", Close: &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyUnconfirmed}}
		}, "control_reply", false},
	})
}

func manyStatus(n int) []adapterproto.RecipientStatus {
	out := make([]adapterproto.RecipientStatus, n)
	for i := range out {
		out[i] = adapterproto.RecipientStatus{To: addr1, State: "queued"}
	}
	return out
}
