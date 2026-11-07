package apiwire_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestPeerProfileIsTheEnginesProfileInProtocolWords(t *testing.T) {
	asOf := time.Date(2026, 11, 7, 2, 0, 5, 0, time.UTC)
	for _, tc := range []struct {
		name string
		in   *engine.ProfileView
		want *adapterproto.PeerProfile
	}{
		{"none known", nil, nil},
		{"every member", &engine.ProfileView{
			Name: "imac", HostName: "imac-1", Platform: "darwin/arm64", System: "macOS 27.0.1",
			SystemBuild: "25A354", SideraVersion: "0.17.0", IssuedAt: asOf.Add(-time.Hour), AsOf: asOf,
		}, &adapterproto.PeerProfile{
			Name: "imac", HostName: "imac-1", Platform: "darwin/arm64", System: "macOS 27.0.1",
			SystemBuild: "25A354", SideraVersion: "0.17.0", AsOf: asOf.Unix(),
		}},
		{"only the time", &engine.ProfileView{AsOf: asOf}, &adapterproto.PeerProfile{AsOf: asOf.Unix()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := apiwire.PeerProfileOf(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PeerProfileOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDirectoryAdapterIsTheEntryAdapterInWireWords(t *testing.T) {
	runtime := &adapterproto.RuntimeInfo{Name: "Codex CLI", Version: "0.163.0"}
	for _, tc := range []struct {
		name string
		in   *transport.DirectoryAdapter
		want *adapterproto.DirectoryAdapter
	}{
		{"no adapter", nil, nil},
		{"every member", &transport.DirectoryAdapter{Instance: "work", ID: "codex", Version: "0.17.0", Runtime: runtime},
			&adapterproto.DirectoryAdapter{Instance: "work", ID: "codex", Version: "0.17.0", Runtime: runtime}},
		{"before the first handshake", &transport.DirectoryAdapter{Instance: "work", ID: "codex"},
			&adapterproto.DirectoryAdapter{Instance: "work", ID: "codex"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := apiwire.DirectoryAdapter(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DirectoryAdapter = %+v, want %+v", got, tc.want)
			}
		})
	}
}
