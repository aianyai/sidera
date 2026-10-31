//go:build netacceptance

package manageapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

type seeded struct {
	signer record.Signer
	node   ident.NodeID
}

func seededNode(t *testing.T, seed byte) seeded {
	t.Helper()
	signer, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	node, err := ident.NodeIDFromKey(signer.Public())
	if err != nil {
		t.Fatal(err)
	}
	return seeded{signer, node}
}

type credentialRecorder struct {
	*fakeService
	rec  record.Record
	node ident.NodeID
}

func (s *credentialRecorder) TestOfferCredential(_ context.Context, node ident.NodeID) (record.Record, error) {
	s.node = node
	return s.rec, nil
}

func (s *credentialRecorder) TestInstallCredential(_ context.Context, node ident.NodeID, rec record.Record) (record.Record, error) {
	s.node = node
	return rec, nil
}

func TestCredentialRowsPassTheNodeAndTheRecordToTheEngine(t *testing.T) {
	one, two := seededNode(t, 1), seededNode(t, 2)
	rec, err := record.Seal(credential.Descriptor, credential.New(one.node, two.node, t0, time.Time{}, credential.Nonce{1}), one.signer)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	svc := &credentialRecorder{fakeService: &fakeService{}, rec: rec}
	r := newRig(t, func(server *Server) { server.cfg.Service = svc })
	node := two.node.String()
	wireJSON, _ := json.Marshal(hex.EncodeToString(wire))

	for name, body := range map[string]string{
		"test.trust.offer":      `{"params":{"node":"` + node + `"}}`,
		"test.trust.credential": `{"params":{"node":"` + node + `","record":` + string(wireJSON) + `}}`,
	} {
		status, out := r.post(name, body)
		var got struct {
			Result struct {
				Node       string `json:"node"`
				Credential string `json:"credential"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if status != 200 || got.Result.Node != node || got.Result.Credential != rec.Digest().String() || svc.node.String() != node {
			t.Errorf("%s: %d %s", name, status, out)
		}
	}
	for name, body := range map[string]string{
		"test.trust.offer":      `{"params":{"node":"not-a-node"}}`,
		"test.trust.credential": `{"params":{"node":"` + node + `","record":"00"}}`,
	} {
		status, out := r.post(name, body)
		r.wantError(400, "invalid_argument", "malformed_request", status, out)
	}
	status, out := r.post("test.trust.credential", `{"params":{"node":"not-a-node","record":`+string(wireJSON)+`}}`)
	r.wantError(400, "invalid_argument", "malformed_request", status, out)
}
