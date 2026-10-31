package nodeproto

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// Golden vectors of every message. The bytes were written by an independent
// CBOR encoder (a short Python program that builds each message from its format
// description, with the keys in canonical order), not by this package. Each
// is encoded from the value and decoded back, so the test pins the wire bytes
// in both directions.

// The vectors, as the fuzz targets seed from them.
const (
	goldenSendRequest       = "a2017823657031717970717870713971637273737a673270767871367273307a716561723773740243e1e2e3"
	goldenSendAnswerOK      = "a3010102815874a30161740241010381a201a20101025820111111111111111111111111111111111111111111111111111111111111111102584022222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222035874a30161740241010381a201a20101025820111111111111111111111111111111111111111111111111111111111111111102584022222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222"
	goldenSendAnswerRefused = "a30102028004706d6573736167655f636f6e666c696374"
	goldenPullRequest       = "a201636d2d310282500102030405060708090a0b0c0d0e0f10502122232425262728292a2b2c2d2e2f30"
	goldenPullAnswerOK      = "a3010102815874a30161740241010381a201a201010258201111111111111111111111111111111111111111111111111111111111111111025840222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222222220381502122232425262728292a2b2c2d2e2f30"
	goldenDirectoryRequest  = "a3017823657031717970717870713971637273737a673270767871367273307a716561723773740263616263031864"
	goldenDirectoryAnswer   = "a301010281a501500102030405060708090a0b0c0d0e0f10026664697265637403644e616d65046005826c6d6573736167652e73656e646373656503626e78"
)

func TestGoldenMessages(t *testing.T) {
	rec := mustRecord(t)
	for _, row := range []struct {
		name   string
		golden string
		encode func() ([]byte, error)
		decode func([]byte) (any, error)
		want   any
	}{
		{
			"send request: {1: sender text, 2: envelope bytes}",
			goldenSendRequest,
			SendRequest{Sender: ep1, Envelope: []byte{0xe1, 0xe2, 0xe3}}.Encode,
			func(b []byte) (any, error) { return DecodeSendRequest(b) },
			SendRequest{Sender: ep1, Envelope: []byte{0xe1, 0xe2, 0xe3}},
		},
		{
			"send answer ok: {1: 1, 2: [report], 3: control reply}",
			goldenSendAnswerOK,
			SendAnswer{Outcome: OutcomeOK, Reports: []record.Record{rec}, ControlReply: &rec}.Encode,
			func(b []byte) (any, error) { return DecodeSendAnswer(b) },
			SendAnswer{Outcome: OutcomeOK, Reports: []record.Record{rec}, ControlReply: &rec},
		},
		{
			"send answer refused: {1: 2, 2: [], 4: reason}",
			goldenSendAnswerRefused,
			SendAnswer{Outcome: OutcomeRefused, Reason: "message_conflict"}.Encode,
			func(b []byte) (any, error) { return DecodeSendAnswer(b) },
			SendAnswer{Outcome: OutcomeRefused, Reports: []record.Record{}, Reason: "message_conflict"},
		},
		{
			"pull request: {1: message id, 2: [endpoint bytes...]}",
			goldenPullRequest,
			PullRequest{Message: "m-1", Recipients: []ident.EndpointID{ep1, ep2}}.Encode,
			func(b []byte) (any, error) { return DecodePullRequest(b) },
			PullRequest{Message: "m-1", Recipients: []ident.EndpointID{ep1, ep2}},
		},
		{
			"pull answer ok: {1: 1, 2: [report], 3: [no-record endpoint]}",
			goldenPullAnswerOK,
			PullAnswer{Outcome: OutcomeOK, Reports: []record.Record{rec}, NoRecord: []ident.EndpointID{ep2}}.Encode,
			func(b []byte) (any, error) { return DecodePullAnswer(b) },
			PullAnswer{Outcome: OutcomeOK, Reports: []record.Record{rec}, NoRecord: []ident.EndpointID{ep2}},
		},
		{
			"directory request: {1: sender text, 2: cursor, 3: limit}",
			goldenDirectoryRequest,
			DirectoryRequest{Sender: ep1, Cursor: "abc", Limit: 100}.Encode,
			func(b []byte) (any, error) { return DecodeDirectoryRequest(b) },
			DirectoryRequest{Sender: ep1, Cursor: "abc", Limit: 100},
		},
		{
			"directory answer ok: {1: 1, 2: [entry], 3: next cursor}",
			goldenDirectoryAnswer,
			DirectoryAnswer{Outcome: OutcomeOK, NextCursor: "nx", Entries: []DirectoryEntry{{
				Endpoint: ep1, Kind: adapterreg.KindDirect, DisplayName: "Name",
				Actions: []authz.Action{authz.ActionMessageSend, authz.ActionSee},
			}}}.Encode,
			func(b []byte) (any, error) { return DecodeDirectoryAnswer(b) },
			DirectoryAnswer{Outcome: OutcomeOK, NextCursor: "nx", Entries: []DirectoryEntry{{
				Endpoint: ep1, Kind: adapterreg.KindDirect, DisplayName: "Name",
				Actions: []authz.Action{authz.ActionMessageSend, authz.ActionSee},
			}}},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			want := unhex(t, row.golden)
			got, err := row.encode()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("encoding differs:\n got %x\nwant %x", got, want)
			}
			v, err := row.decode(want)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(v, row.want) {
				t.Errorf("decoded %+v, want %+v", v, row.want)
			}
		})
	}
}
