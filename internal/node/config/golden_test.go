package config_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
)

// The golden files are written out by hand in fixtures_test.go. Each decodes to
// the value written out here, field by field, and the value encodes back to the
// file byte for byte: the encoding is canonical, so what the node writes is
// what an owner reads and edits.

func goldenOperation(t testing.TB) *config.Operation {
	t.Helper()
	var id admin.OperationID
	for i := range id {
		id[i] = byte(i<<4 | i)
	}
	var fp admin.Fingerprint
	for i := range fp {
		fp[i] = byte(i + 1)
	}
	return &config.Operation{ID: id, Fingerprint: fp, Result: json.RawMessage(`{"version":3}`)}
}

func TestGoldenNode(t *testing.T) {
	want := config.Node{
		Version: 3,
		Settings: config.NodeSettings{
			ReplyPermission: true, NodeQueueLimit: 10000, DiskReserveBytes: 268435456, NodePromptTurns: 8,
			ArchiveRetentionDays: 30, LogTotalBytes: 157286400,
			EndpointDefaults: config.EndpointSettings{MaxQueue: 1800 * time.Second, BacklogLimit: 200},
		},
		LastOperation: goldenOperation(t),
	}
	got, err := config.DecodeNode([]byte(goldenNode))
	if err != nil {
		t.Fatalf("DecodeNode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeNode = %+v\nwant %+v", got, want)
	}
	enc, err := want.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) != goldenNode {
		t.Errorf("Encode =\n%s\nwant\n%s", enc, goldenNode)
	}
	// A file an owner edited by hand has no last operation, and any layout.
	hand := `{"format":1,"version":1,"settings":{"reply_permission":false,"node_queue_limit":5,"disk_reserve_bytes":268435456,` +
		`"node_prompt_turns":2,"archive_retention_days":1,"log_total_bytes":1048576,"endpoint_defaults":{"max_queue_s":60,"backlog_limit":3}}}`
	n, err := config.DecodeNode([]byte(hand))
	if err != nil || n.LastOperation != nil || n.Settings.ReplyPermission || n.Settings.NodeQueueLimit != 5 {
		t.Errorf("hand edited node = %+v, %v", n, err)
	}
}

func TestGoldenPolicy(t *testing.T) {
	n2 := mustNode(t, node2Text)
	n1 := mustNode(t, node1Text)
	ep1, ep2 := mustEndpoint(t, ep1Text), mustEndpoint(t, ep2Text)
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	notBefore := time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC)
	rules := []authz.Rule{
		authz.Allow(authz.Members(), authz.AllLocalEndpoints(), authz.ActionSee, authz.Conditions{}),
		authz.Deny(authz.AnyEndpointOf(n2), authz.LocalEndpoint(ep2), authz.ActionMessageSend, authz.Conditions{NotAfter: notAfter}),
		authz.Allow(authz.DirectTrusted(), authz.RuntimeEndpoint(ep1), authz.ActionSessionCreate,
			authz.Conditions{Rate: authz.RateCap{Count: 5, Window: time.Minute}, Presets: []string{"default"}}),
		authz.Allow(authz.EndpointOf(ident.Address{Node: n2, Endpoint: ep2}), authz.AllLocalEndpoints(), authz.ActionMessageSend,
			authz.Conditions{NotBefore: notBefore, MaxBodyBytes: 1024}),
		authz.OnlyFrom(authz.ThisNode(), authz.ActionRelayReserve, n1, n2),
	}
	got, err := config.DecodePolicy([]byte(goldenPolicy))
	if err != nil {
		t.Fatalf("DecodePolicy: %v", err)
	}
	if got.Version != 2 || got.LastOperation != nil {
		t.Errorf("version %d, last operation %v", got.Version, got.LastOperation)
	}
	if !reflect.DeepEqual(got.Policy.Rules(), rules) {
		t.Errorf("rules = %+v\nwant %+v", got.Policy.Rules(), rules)
	}
	pol, err := authz.NewPolicy(rules)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := (config.PolicyFile{Version: 2, Policy: pol}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) != goldenPolicy {
		t.Errorf("Encode =\n%s\nwant\n%s", enc, goldenPolicy)
	}
}

// Every action, effect, subject and object the policy knows has a name in the
// file, and the names are the ones written here: the file is how an owner
// states a rule, so a name changing would change what an old file means.
func TestPolicyNamesAreStable(t *testing.T) {
	n := mustNode(t, node1Text)
	ep := mustEndpoint(t, ep1Text)
	actions := map[authz.Action]string{
		authz.ActionSee: "see", authz.ActionMessageSend: "message.send", authz.ActionSessionCreate: "session.create",
		authz.ActionSessionClose: "session.close", authz.ActionSessionInterrupt: "session.interrupt",
		authz.ActionEndpointStatus: "endpoint.status", authz.ActionRelayReserve: "relay.reserve", authz.ActionRelayConnect: "relay.connect",
	}
	if len(actions) != len(authz.Actions) {
		t.Fatalf("the test knows %d actions, authz has %d: name the new one here", len(actions), len(authz.Actions))
	}
	for a, name := range actions {
		obj := authz.AllLocalEndpoints()
		switch a {
		case authz.ActionRelayReserve, authz.ActionRelayConnect:
			obj = authz.ThisNode()
		}
		p, err := authz.NewPolicy([]authz.Rule{authz.Allow(authz.Members(), obj, a, authz.Conditions{})})
		if err != nil {
			t.Fatal(err)
		}
		enc, err := (config.PolicyFile{Version: 1, Policy: p}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(enc), `"action": "`+name+`"`) {
			t.Errorf("action %v is not written as %q:\n%s", a, name, enc)
		}
	}
	subjects := map[string]authz.Subject{
		"member": authz.Members(), "direct": authz.DirectTrusted(), "node": authz.AnyEndpointOf(n),
		"endpoint": authz.EndpointOf(ident.Address{Node: n, Endpoint: ep}),
	}
	for name, s := range subjects {
		p, _ := authz.NewPolicy([]authz.Rule{authz.Deny(s, authz.AllLocalEndpoints(), authz.ActionSee, authz.Conditions{})})
		enc, _ := (config.PolicyFile{Version: 1, Policy: p}).Encode()
		if !strings.Contains(string(enc), `"kind": "`+name+`"`) {
			t.Errorf("subject %q is not written as such:\n%s", name, enc)
		}
	}
	objects := map[string]authz.Object{
		"endpoint": authz.LocalEndpoint(ep), "all_endpoints": authz.AllLocalEndpoints(), "runtime": authz.RuntimeEndpoint(ep), "node": authz.ThisNode(),
	}
	for name, o := range objects {
		action := authz.ActionSee
		p, _ := authz.NewPolicy([]authz.Rule{authz.Allow(authz.Members(), o, action, authz.Conditions{})})
		enc, _ := (config.PolicyFile{Version: 1, Policy: p}).Encode()
		if !strings.Contains(string(enc), `"kind": "`+name+`"`) {
			t.Errorf("object %q is not written as such:\n%s", name, enc)
		}
	}
}

// The policy `init` writes is the default policy of authz, and nothing else.
func TestDefaultPolicyRoundTrips(t *testing.T) {
	def := authz.DefaultPolicy()
	enc, err := (config.PolicyFile{Version: 1, Policy: def}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.DecodePolicy(enc)
	if err != nil {
		t.Fatalf("DecodePolicy of the default policy: %v", err)
	}
	if !reflect.DeepEqual(got.Policy.Rules(), def.Rules()) {
		t.Errorf("the default policy does not survive the file:\n got %+v\nwant %+v", got.Policy.Rules(), def.Rules())
	}
}

func TestGoldenPresets(t *testing.T) {
	want := config.Presets{
		Version: 2,
		Items: map[string]config.Preset{
			"default": {LocalSources: authz.PresetLocalAllow, Description: "Created by sidera init."},
			"sandboxed": {LocalSources: authz.PresetLocalDeny, Config: map[string]map[string]json.RawMessage{
				"example": {"model": json.RawMessage(`"small"`), "turns": json.RawMessage(`4`)},
			}},
		},
	}
	got, err := config.DecodePresets([]byte(goldenPresets))
	if err != nil {
		t.Fatalf("DecodePresets: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodePresets = %+v\nwant %+v", got, want)
	}
	enc, err := want.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) != goldenPresets {
		t.Errorf("Encode =\n%s\nwant\n%s", enc, goldenPresets)
	}
}

func TestGoldenInstances(t *testing.T) {
	ep1, ep3 := mustEndpoint(t, ep1Text), mustEndpoint(t, ep3Text)
	want := config.Instances{
		Version: 4,
		Items: map[string]config.Instance{
			"example": {
				Adapter: "example", Enabled: true,
				Config: map[string]json.RawMessage{"workspace": json.RawMessage(`"/Users/me/work"`)},
				Endpoints: map[ident.EndpointID]config.EndpointOverride{
					ep1: {BacklogLimit: 50},
					ep3: {MaxQueue: time.Hour, BacklogLimit: 400},
				},
			},
		},
	}
	got, err := config.DecodeInstances([]byte(goldenInstances))
	if err != nil {
		t.Fatalf("DecodeInstances: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeInstances = %+v\nwant %+v", got, want)
	}
	enc, err := want.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) != goldenInstances {
		t.Errorf("Encode =\n%s\nwant\n%s", enc, goldenInstances)
	}
}

func TestGoldenEndpointIDs(t *testing.T) {
	want := config.EndpointIDs{
		Runtime: mustEndpoint(t, ep1Text),
		Direct:  map[string]ident.EndpointID{"notes": mustEndpoint(t, ep2Text)},
	}
	got, err := config.DecodeEndpointIDs("example", []byte(goldenEndpointIDs))
	if err != nil {
		t.Fatalf("DecodeEndpointIDs: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeEndpointIDs = %+v, want %+v", got, want)
	}
	enc, err := want.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) != goldenEndpointIDs {
		t.Errorf("Encode =\n%s\nwant\n%s", enc, goldenEndpointIDs)
	}
	// An adapter without a runtime endpoint has direct endpoints only, and one
	// with neither has an empty file.
	direct := config.EndpointIDs{Direct: map[string]ident.EndpointID{"a": mustEndpoint(t, ep2Text)}}
	enc, _ = direct.Encode()
	if want := "{\n  \"format\": 1,\n  \"direct\": {\n    \"a\": \"" + ep2Text + "\"\n  }\n}\n"; string(enc) != want {
		t.Errorf("direct only =\n%s\nwant\n%s", enc, want)
	}
}

func TestGoldenBackup(t *testing.T) {
	_, digest, _ := selfRevocation(t, 7)
	want := config.Backup{ConfirmedAt: time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC), Digest: digest}
	doc := strings.Replace(goldenBackup, "%s", digest.String(), 1)
	got, err := config.DecodeBackup([]byte(doc))
	if err != nil {
		t.Fatalf("DecodeBackup: %v", err)
	}
	if !got.ConfirmedAt.Equal(want.ConfirmedAt) || got.Digest != want.Digest {
		t.Errorf("DecodeBackup = %+v, want %+v", got, want)
	}
	enc, err := want.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) != doc {
		t.Errorf("Encode =\n%s\nwant\n%s", enc, doc)
	}
}

func TestGoldenAdapterCurrent(t *testing.T) {
	v, err := config.DecodeAdapterCurrent("example", []byte(goldenCurrent))
	if err != nil || v != "1.0.0" {
		t.Fatalf("DecodeAdapterCurrent = %q, %v", v, err)
	}
	enc, err := config.EncodeAdapterCurrent("1.0.0")
	if err != nil || string(enc) != goldenCurrent {
		t.Errorf("EncodeAdapterCurrent = %q, %v, want %q", enc, err, goldenCurrent)
	}
}

// The person that sent an operation is "local" for the owner and the person id
// for a remote person; both survive the file.
func TestOperationPersonRoundTrips(t *testing.T) {
	doc := strings.Replace(goldenNode, `"person": "local"`, `"person": "`+personText+`"`, 1)
	n, err := config.DecodeNode([]byte(doc))
	if err != nil {
		t.Fatalf("DecodeNode: %v", err)
	}
	if n.LastOperation.Person.IsZero() || n.LastOperation.Person.String() != personText {
		t.Fatalf("person = %v", n.LastOperation.Person)
	}
	enc, err := n.Encode()
	if err != nil || string(enc) != doc {
		t.Errorf("Encode = %s, %v", enc, err)
	}
}
