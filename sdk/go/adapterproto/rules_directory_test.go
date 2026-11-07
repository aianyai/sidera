package adapterproto_test

import (
	"fmt"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func entry() adapterproto.DirectoryEntry {
	return adapterproto.DirectoryEntry{Address: addr1, Kind: adapterproto.EndpointDirect, DisplayName: "Owner console",
		Description: "reads for a person", Accepts: []adapterproto.EndpointAction{adapterproto.ActionMessageSend, adapterproto.ActionEndpointStatus}}
}

func TestDirectoryParamsRules(t *testing.T) {
	method(t, func() adapterproto.DirectoryParams { return adapterproto.DirectoryParams{Endpoint: ep1} },
		[]row[adapterproto.DirectoryParams]{
			{"endpoint required", func(p *adapterproto.DirectoryParams) { p.Endpoint = "" }, "endpoint", false},
			{"node, cursor and limit absent", nil, "", false},
			{"node given", func(p *adapterproto.DirectoryParams) { p.Node = "node-b" }, "", false},
			{"node of 128 characters", func(p *adapterproto.DirectoryParams) { p.Node = repeat("n", 128) }, "", false},
			{"node of 129 characters", func(p *adapterproto.DirectoryParams) { p.Node = repeat("n", 129) }, "node", false},
			{"cursor of 256 characters", func(p *adapterproto.DirectoryParams) { p.Cursor = repeat("c", 256) }, "", false},
			{"cursor of 257 characters", func(p *adapterproto.DirectoryParams) { p.Cursor = repeat("c", 257) }, "cursor", false},
			{"limit 1", func(p *adapterproto.DirectoryParams) { p.Limit = 1 }, "", false},
			{"limit 100", func(p *adapterproto.DirectoryParams) { p.Limit = 100 }, "", false},
			{"limit 101 would not fit a line", func(p *adapterproto.DirectoryParams) { p.Limit = 101 }, "limit", false},
			{"limit negative", func(p *adapterproto.DirectoryParams) { p.Limit = -1 }, "limit", false},
		})
}

func TestDirectoryEntryRules(t *testing.T) {
	method(t, entry, []row[adapterproto.DirectoryEntry]{
		{"address required", func(e *adapterproto.DirectoryEntry) { e.Address = "" }, "address", false},
		{"every kind", func(e *adapterproto.DirectoryEntry) {
			for _, k := range []adapterproto.EndpointKind{"runtime", "session", "direct"} {
				e.Kind = k
				if err := e.Validate(); err != nil {
					panic(err)
				}
			}
		}, "", false},
		{"kind unknown", func(e *adapterproto.DirectoryEntry) { e.Kind = "system" }, "kind", false},
		{"kind required", func(e *adapterproto.DirectoryEntry) { e.Kind = "" }, "kind", false},
		{"display name and description are optional", func(e *adapterproto.DirectoryEntry) { e.DisplayName, e.Description = "", "" }, "", false},
		{"display name keeps the display-name rule", func(e *adapterproto.DirectoryEntry) { e.DisplayName = "a‮b" }, "display_name", false},
		{"display name of 129 characters", func(e *adapterproto.DirectoryEntry) { e.DisplayName = repeat("n", 129) }, "display_name", false},
		{"description of 1024 characters", func(e *adapterproto.DirectoryEntry) { e.Description = repeat("d", 1024) }, "", false},
		{"description of 1025 characters", func(e *adapterproto.DirectoryEntry) { e.Description = repeat("d", 1025) }, "description", false},
		{"adapter is optional", func(e *adapterproto.DirectoryEntry) { e.Adapter = nil }, "", false},
		{"adapter with every member", func(e *adapterproto.DirectoryEntry) { e.Adapter = fullAdapter() }, "", false},
		{"adapter with only its instance and id", func(e *adapterproto.DirectoryEntry) {
			e.Adapter = &adapterproto.DirectoryAdapter{Instance: "work", ID: "codex"}
		}, "", false},
		{"adapter without a runtime", func(e *adapterproto.DirectoryEntry) { e.Adapter = fullAdapter(); e.Adapter.Runtime = nil }, "", false},
		{"adapter instance required", func(e *adapterproto.DirectoryEntry) { e.Adapter = fullAdapter(); e.Adapter.Instance = "" }, "adapter.instance", false},
		{"adapter instance is an adapter identifier", func(e *adapterproto.DirectoryEntry) { e.Adapter = fullAdapter(); e.Adapter.Instance = "Work" }, "adapter.instance", false},
		{"adapter id required", func(e *adapterproto.DirectoryEntry) { e.Adapter = fullAdapter(); e.Adapter.ID = "" }, "adapter.id", false},
		{"adapter id is an adapter identifier", func(e *adapterproto.DirectoryEntry) { e.Adapter = fullAdapter(); e.Adapter.ID = "co dex" }, "adapter.id", false},
		{"adapter version is a semantic version", func(e *adapterproto.DirectoryEntry) { e.Adapter = fullAdapter(); e.Adapter.Version = "v1" }, "adapter.version", false},
		{"adapter runtime is validated", func(e *adapterproto.DirectoryEntry) { e.Adapter = fullAdapter(); e.Adapter.Runtime.Name = "" }, "adapter.runtime.name", false},
		{"adapter runtime version keeps the display-name rule", func(e *adapterproto.DirectoryEntry) {
			e.Adapter = fullAdapter()
			e.Adapter.Runtime.Version = "1\u202e"
		}, "adapter.runtime.version", false},
		{"accepts required", func(e *adapterproto.DirectoryEntry) { e.Accepts = nil }, "accepts", false},
		{"accepts may be empty", func(e *adapterproto.DirectoryEntry) { e.Accepts = []adapterproto.EndpointAction{} }, "", false},
		{"every action", func(e *adapterproto.DirectoryEntry) {
			e.Accepts = []adapterproto.EndpointAction{"message.send", "session.create", "session.close", "session.interrupt", "endpoint.status"}
		}, "", false},
		{"see is not an action an entry advertises", func(e *adapterproto.DirectoryEntry) { e.Accepts = []adapterproto.EndpointAction{"see"} }, "accepts[0]", false},
		{"an action twice", func(e *adapterproto.DirectoryEntry) {
			e.Accepts = []adapterproto.EndpointAction{"message.send", "message.send"}
		}, "accepts", false},
	})
}

func fullAdapter() *adapterproto.DirectoryAdapter {
	return &adapterproto.DirectoryAdapter{Instance: "work", ID: "codex", Version: "0.17.0",
		Runtime: &adapterproto.RuntimeInfo{Name: "Codex CLI", Version: "0.163.0"}}
}

func manyEntries(n int) []adapterproto.DirectoryEntry {
	out := make([]adapterproto.DirectoryEntry, n)
	for i := range out {
		out[i] = entry()
	}
	return out
}

func TestDirectoryResultRules(t *testing.T) {
	ok := func() adapterproto.DirectoryResult {
		return adapterproto.DirectoryResult{Outcome: adapterproto.DirectoryOK, Endpoints: manyEntries(2), NextCursor: "c2"}
	}
	method(t, ok, []row[adapterproto.DirectoryResult]{
		{"outcome required", func(r *adapterproto.DirectoryResult) { r.Outcome = "" }, "outcome", false},
		{"outcome unknown", func(r *adapterproto.DirectoryResult) { r.Outcome = "failed" }, "outcome", false},
		{"the last page has no cursor", func(r *adapterproto.DirectoryResult) { r.NextCursor = "" }, "", false},
		{"an empty page", func(r *adapterproto.DirectoryResult) { r.Endpoints, r.NextCursor = []adapterproto.DirectoryEntry{}, "" }, "", false},
		{"endpoints required", func(r *adapterproto.DirectoryResult) { r.Endpoints = nil }, "endpoints", false},
		{"100 entries", func(r *adapterproto.DirectoryResult) { r.Endpoints = manyEntries(100) }, "", false},
		{"101 entries would not fit a line", func(r *adapterproto.DirectoryResult) { r.Endpoints = manyEntries(101) }, "endpoints", false},
		{"an entry is validated", func(r *adapterproto.DirectoryResult) { r.Endpoints[1].Kind = "x" }, "endpoints[1].kind", false},
		{"cursor over the limit", func(r *adapterproto.DirectoryResult) { r.NextCursor = repeat("c", 257) }, "next_cursor", false},
		{"a page has no reason", func(r *adapterproto.DirectoryResult) { r.Reason = "directory_unavailable" }, "reason", false},
	})
	for _, outcome := range []adapterproto.DirectoryOutcome{adapterproto.DirectoryUnavailable, adapterproto.DirectoryRefused} {
		t.Run(string(outcome), func(t *testing.T) {
			method(t, func() adapterproto.DirectoryResult {
				return adapterproto.DirectoryResult{Outcome: outcome, Reason: "directory_unavailable", Endpoints: []adapterproto.DirectoryEntry{}}
			}, []row[adapterproto.DirectoryResult]{
				{"a reason word", nil, "", false},
				{"a reason the target named", func(r *adapterproto.DirectoryResult) { r.Reason = "shutting_down" }, "", false},
				{"the reason is required", func(r *adapterproto.DirectoryResult) { r.Reason = "" }, "reason", false},
				{"the reason is a snake_case word", func(r *adapterproto.DirectoryResult) { r.Reason = "Not A Word" }, "reason", false},
				{"the reason is at most 64 characters", func(r *adapterproto.DirectoryResult) { r.Reason = adapterproto.DirectoryReason(repeat("a", 65)) }, "reason", false},
				{"no page comes with it", func(r *adapterproto.DirectoryResult) { r.Endpoints = manyEntries(1) }, "endpoints", false},
				{"no cursor comes with it", func(r *adapterproto.DirectoryResult) { r.NextCursor = "c" }, "next_cursor", false},
				{"endpoints required", func(r *adapterproto.DirectoryResult) { r.Endpoints = nil }, "endpoints", false},
			})
		})
	}
}

// TestDirectoryPageFitsALine: the largest page the result accepts stays under the
// line limit, with the longest entries the rules allow. A description of 1024
// characters takes the most bytes as `<`, which Encode (encoding/json with HTML
// escaping) writes as six, or as a character of four bytes, which is the most the
// wire encoder (no HTML escaping) writes; a line feed and a tab, the only control
// characters it may hold, take two. The test measures both.
func TestDirectoryPageFitsALine(t *testing.T) {
	for _, c := range []struct{ name, char string }{
		{"six bytes escaped", "<"},
		{"four bytes as they are", "\U0001F600"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := adapterproto.DirectoryEntry{Address: adapterproto.Address(repeat("a", 128)), Kind: adapterproto.EndpointSession,
				DisplayName: repeat(c.char, 128), Description: repeat(c.char, adapterproto.MaxDescription),
				Adapter: &adapterproto.DirectoryAdapter{Instance: repeat("a", 32), ID: repeat("a", 32), Version: "1." + repeat("2", 8) + "." + repeat("3", 8) + "-" + repeat("p", 40),
					Runtime: &adapterproto.RuntimeInfo{Name: repeat(c.char, 64), Version: repeat(c.char, 64)}},
				Accepts: []adapterproto.EndpointAction{"message.send", "session.create", "session.close", "session.interrupt", "endpoint.status"}}
			r := adapterproto.DirectoryResult{Outcome: adapterproto.DirectoryOK, Endpoints: make([]adapterproto.DirectoryEntry, adapterproto.MaxDirectoryLimit), NextCursor: repeat("c", adapterproto.MaxCursorLen)}
			for i := range r.Endpoints {
				r.Endpoints[i] = e
			}
			b, err := adapterproto.Encode(r)
			if err != nil {
				t.Fatal(err)
			}
			const line = 1 << 20
			if len(b) >= line {
				t.Fatalf("a page of %d entries is %d bytes, over the line limit %d", adapterproto.MaxDirectoryLimit, len(b), line)
			}
			t.Logf("the largest page is %d bytes", len(b))
		})
	}
}

func TestPeersRules(t *testing.T) {
	method(t, func() adapterproto.PeersParams { return adapterproto.PeersParams{Endpoint: ep1} },
		[]row[adapterproto.PeersParams]{{"endpoint required", func(p *adapterproto.PeersParams) { p.Endpoint = "" }, "endpoint", false}})
	method(t, func() adapterproto.PeersResult {
		return adapterproto.PeersResult{Nodes: []adapterproto.PeerNode{
			{Node: "node-a", Self: true, Trust: adapterproto.PeerSelf, Connected: true},
			{Node: "node-b", Trust: adapterproto.PeerDirect},
		}}
	}, []row[adapterproto.PeersResult]{
		{"nodes required", func(r *adapterproto.PeersResult) { r.Nodes = nil }, "nodes", false},
		{"nodes may be empty", func(r *adapterproto.PeersResult) { r.Nodes = []adapterproto.PeerNode{} }, "", false},
		{"node required", func(r *adapterproto.PeersResult) { r.Nodes[1].Node = "" }, "nodes[1].node", false},
		{"node of 129 characters", func(r *adapterproto.PeersResult) { r.Nodes[1].Node = repeat("n", 129) }, "nodes[1].node", false},
		{"trust required", func(r *adapterproto.PeersResult) { r.Nodes[1].Trust = "" }, "nodes[1].trust", false},
		{"trust unknown", func(r *adapterproto.PeersResult) { r.Nodes[1].Trust = "relayed" }, "nodes[1].trust", false},
		{"self goes with trust self", func(r *adapterproto.PeersResult) { r.Nodes[1].Self = true }, "nodes[1].trust", false},
		{"profile is optional", func(r *adapterproto.PeersResult) { r.Nodes[0].Profile = nil }, "", false},
		{"profile with every member", func(r *adapterproto.PeersResult) { r.Nodes[0].Profile = fullProfile() }, "", false},
		{"profile with only the time", func(r *adapterproto.PeersResult) { r.Nodes[1].Profile = &adapterproto.PeerProfile{AsOf: 1792713595} }, "", false},
		{"profile as_of required", func(r *adapterproto.PeersResult) { r.Nodes[1].Profile = fullProfile(); r.Nodes[1].Profile.AsOf = 0 }, "nodes[1].profile.as_of", false},
		{"profile as_of beyond the year 9999", func(r *adapterproto.PeersResult) {
			r.Nodes[1].Profile = fullProfile()
			r.Nodes[1].Profile.AsOf = adapterproto.MaxTimestampS + 1
		}, "nodes[1].profile.as_of", false},
		{"profile name of 129 characters", func(r *adapterproto.PeersResult) {
			r.Nodes[1].Profile = fullProfile()
			r.Nodes[1].Profile.Name = repeat("n", 129)
		}, "nodes[1].profile.name", false},
		{"trust self goes with self", func(r *adapterproto.PeersResult) { r.Nodes[0].Self = false }, "nodes[0].trust", false},
	})
}

func fullProfile() *adapterproto.PeerProfile {
	return &adapterproto.PeerProfile{Name: "imac", HostName: "imac-1", Platform: "darwin/arm64", System: "macOS 27.0.1",
		SystemBuild: "25A354", SideraVersion: "0.17.0", AsOf: 1792713595}
}

func TestDeclareRules(t *testing.T) {
	method(t, func() adapterproto.DeclareParams {
		return adapterproto.DeclareParams{Endpoint: ep1, Paused: ptr(true), AcceptFrom: &[]adapterproto.Address{addr1}}
	}, []row[adapterproto.DeclareParams]{
		{"endpoint required", func(p *adapterproto.DeclareParams) { p.Endpoint = "" }, "endpoint", false},
		{"every member", func(p *adapterproto.DeclareParams) {
			p.RefuseMultiRecipient, p.RestrictSenders, p.HiddenFromDirectory = ptr(false), ptr(true), ptr(true)
		}, "", false},
		{"an explicit empty list clears the senders", func(p *adapterproto.DeclareParams) { p.AcceptFrom = &[]adapterproto.Address{} }, "", false},
		{"a list that is nil is missing", func(p *adapterproto.DeclareParams) { p.AcceptFrom = new([]adapterproto.Address) }, "accept_from", false},
		{"a sender is validated", func(p *adapterproto.DeclareParams) { p.AcceptFrom = &[]adapterproto.Address{""} }, "accept_from[0]", false},
		// The node's rules, which the engine judges and answers with a reason
		// in the data of -32602, are not the message's.
		{"an empty patch is the node's to refuse", func(p *adapterproto.DeclareParams) { *p = adapterproto.DeclareParams{Endpoint: ep1} }, "", false},
		{"more senders than a node allows are the node's to refuse", func(p *adapterproto.DeclareParams) {
			l := manyAddrs(300)
			p.AcceptFrom = &l
		}, "", false},
	})
	method(t, func() adapterproto.DeclareResult {
		return adapterproto.DeclareResult{Declaration: adapterproto.Declaration{AcceptFrom: []adapterproto.Address{}}}
	}, []row[adapterproto.DeclareResult]{
		{"the list may be empty", nil, "", false},
		{"the list is required", func(r *adapterproto.DeclareResult) { r.Declaration.AcceptFrom = nil }, "declaration.accept_from", false},
		{"a sender is validated", func(r *adapterproto.DeclareResult) { r.Declaration.AcceptFrom = []adapterproto.Address{""} }, "declaration.accept_from[0]", false},
	})
}

// TestParamsReasonTable: the reasons of invalid params, in order. One is
// appended, never renamed.
func TestParamsReasonTable(t *testing.T) {
	want := []string{"cursor_invalid", "empty_patch", "too_many_senders", "duplicate_sender"}
	if got := adapterproto.ParamsReasons(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ParamsReasons = %v, want %v", got, want)
	}
	if got := adapterproto.DirectoryReasons(); fmt.Sprint(got) != "[directory_unavailable]" {
		t.Fatalf("DirectoryReasons = %v", got)
	}
}
