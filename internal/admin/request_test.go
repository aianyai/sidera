package admin

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
)

func TestValidateRequest(t *testing.T) {
	id := OperationID{1}
	tests := []struct {
		name string
		req  Request
		want Reason
	}{
		{"a read needs no operation ID", Request{Op: Operation{Kind: OpRead}}, ReasonOK},
		{"querying an operation's result needs no operation ID of its own",
			Request{Op: Operation{Kind: OpQueryOperationResult}}, ReasonOK},
		{"switching LAN discovery is a configuration change",
			Request{ID: id, Op: Operation{Kind: OpSetLANDiscovery}}, ReasonMissingExpectedVersion},
		{"a preset change is a configuration change",
			Request{ID: id, Op: Operation{Kind: OpChangePreset, PresetChange: PresetChangeNarrowing}}, ReasonMissingExpectedVersion},
		{"rejecting a trust request is a one-off decision and carries no version",
			Request{ID: id, Op: Operation{Kind: OpRejectTrustRequest}}, ReasonOK},
		{"rejecting a trust request still needs an operation ID",
			Request{Op: Operation{Kind: OpRejectTrustRequest}}, ReasonMissingOperationID},
		{"a side-effecting request without an operation ID is refused",
			Request{Op: Operation{Kind: OpRevokeTrust}}, ReasonMissingOperationID},
		{"a side-effecting request with an operation ID is well formed",
			Request{ID: id, Op: Operation{Kind: OpRevokeTrust}}, ReasonOK},
		{"a configuration change without an expected version is refused",
			Request{ID: id, Op: Operation{Kind: OpEditDeclaration}}, ReasonMissingExpectedVersion},
		{"a configuration change with an expected version is well formed",
			Request{ID: id, Op: Operation{Kind: OpEditDeclaration}, ExpectedVersion: 3}, ReasonOK},
		{"a policy change is a configuration change",
			Request{ID: id, Op: Operation{Kind: OpChangePolicy, PolicyChange: PolicyChangeTightening}}, ReasonMissingExpectedVersion},
		{"a missing operation ID is reported before a missing version",
			Request{Op: Operation{Kind: OpChangeRelaySettings}}, ReasonMissingOperationID},
		{"trust-edge operations carry no version",
			Request{ID: id, Op: Operation{Kind: OpLocallyRevokeNode}}, ReasonOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidateRequest(tc.req); got != tc.want {
				t.Fatalf("ValidateRequest = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestEveryConfigChangeIsSideEffecting(t *testing.T) {
	for _, tc := range catalogue {
		if tc.op.IsConfigChange() && !tc.op.SideEffecting() {
			t.Errorf("%s: a configuration change must be side-effecting", tc.name)
		}
	}
	for _, kind := range []OpKind{OpRead, OpQueryOperationResult, OpUnknown} {
		if (Operation{Kind: kind}).SideEffecting() {
			t.Errorf("operation kind %d must not be side-effecting", kind)
		}
	}
	if !(Operation{Kind: OpRejectTrustRequest}).SideEffecting() {
		t.Error("rejecting a trust request must be side-effecting")
	}
}

func TestConfigChangesAreTheVersionedSettings(t *testing.T) {
	want := map[OpKind]bool{
		OpEnableInheritance: true, OpDisableInheritance: true,
		OpExtendGracePeriod: true, OpShortenGracePeriod: true,
		OpChangePolicy: true, OpChangePreset: true,
		OpEditDeclaration: true, OpChangeRelaySettings: true,
		OpSetLANDiscovery: true,
	}
	for _, tc := range catalogue {
		if got := tc.op.IsConfigChange(); got != want[tc.op.Kind] {
			t.Errorf("%s: IsConfigChange = %v, want %v", tc.name, got, want[tc.op.Kind])
		}
	}
}

func TestCheckVersion(t *testing.T) {
	tests := []struct {
		name              string
		expected, current Version
		want              VersionVerdict
	}{
		{"an expected version equal to the current one applies", 4, 4, VersionVerdict{Apply: true, Reason: ReasonOK}},
		{"a stale expected version conflicts and is never applied", 3, 4, VersionVerdict{Reason: ReasonVersionConflict}},
		{"an expected version ahead of the current one conflicts too", 5, 4, VersionVerdict{Reason: ReasonVersionConflict}},
		{"an absent expected version never matches an existing setting", 0, 1, VersionVerdict{Reason: ReasonVersionConflict}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckVersion(tc.expected, tc.current); got != tc.want {
				t.Fatalf("CheckVersion = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCheckReplay(t *testing.T) {
	fpA, fpB := Fingerprint{1}, Fingerprint{2}
	tests := []struct {
		name   string
		stored *StoredRequest
		in     Fingerprint
		want   ReplayVerdict
	}{
		{"a new operation ID is executed", nil, fpA, ReplayVerdict{ReplayExecute, ReasonFirstSeen}},
		{"the same request under a seen ID returns the stored result", &StoredRequest{fpA}, fpA,
			ReplayVerdict{ReplayReturnStored, ReasonSameRequest}},
		{"a different request under a seen ID is a conflict, not a stored result", &StoredRequest{fpA}, fpB,
			ReplayVerdict{ReplayConflict, ReasonOperationIDReused}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckReplay(tc.stored, tc.in); got != tc.want {
				t.Fatalf("CheckReplay = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestKeyForScopesOperationIDsToThePerson(t *testing.T) {
	a, b := person(t, 1), person(t, 2)
	id := OperationID{7}
	cert := validCert(LevelManage)
	keys := map[string]RequestKey{
		"owner": KeyFor(LocalOwner(), id),
		"a":     KeyFor(RemotePerson(a, cert), id),
		"b":     KeyFor(RemotePerson(b, cert), id),
	}
	if keys["a"] == keys["b"] || keys["a"] == keys["owner"] || keys["b"] == keys["owner"] {
		t.Fatalf("the same operation ID from different callers must give different keys: %v", keys)
	}
	if keys["a"] != KeyFor(RemotePerson(a, cert), id) {
		t.Fatal("the same caller and ID must give the same key")
	}
}

func TestNewOperationID(t *testing.T) {
	t.Run("a random source yields the bytes it gave", func(t *testing.T) {
		src := bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
		id, err := NewOperationID(src)
		if err != nil {
			t.Fatal(err)
		}
		if want := (OperationID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}); id != want {
			t.Fatalf("id = %v, want %v", id, want)
		}
	})
	t.Run("an all-zero draw is refused because zero means none", func(t *testing.T) {
		id, err := NewOperationID(bytes.NewReader(make([]byte, 16)))
		if !errors.Is(err, ident.ErrZero) || !id.IsZero() {
			t.Fatalf("id, err = %v, %v; want zero ID and ErrZero", id, err)
		}
	})
	t.Run("a source that runs dry is an error", func(t *testing.T) {
		if _, err := NewOperationID(strings.NewReader("short")); err == nil {
			t.Fatal("want an error for a short read")
		}
	})
}
