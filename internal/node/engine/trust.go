package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// This file is what the engine keeps of the trust records of state/trust/ and
// state/peers/: the credentials this node holds, the revocations of credentials,
// the nodes it removed from its own trust and the certificates of its peers. The
// store holds them as structure only and never checks a signature, so every
// record is verified here before it counts, and a stored record that does not
// verify is as if it were not there (it is logged, and it is not repaired: it is
// the owner's file, not the engine's to remove).
//
// The state belongs to the loop. It is changed only after the write that makes
// the change durable has returned, and every change goes through trustChanged, so
// the view that trust.Evaluate reads and the snapshot the network is given can
// never disagree with the disk.

// heldCredential is a credential of this node, with the record it was stored as.
type heldCredential struct {
	rec  store.CredentialRecord
	cred record.Verified[credential.Credential]
}

// heldPeer is the certificate accepted from a peer, verified, with the record it
// is stored as, which also holds the highest serial seen for the node.
type heldPeer struct {
	rec  store.PeerRecord
	cert record.Verified[record.NetworkKeyCertificate]
}

// seen is the serial state the record carries.
func (p heldPeer) seen() record.SerialSeen {
	return record.SerialSeen{Node: p.rec.Node, Serial: p.rec.SerialSeen, Digest: p.rec.SeenDigest}
}

// trustState is the verified content of the trust and peer records.
type trustState struct {
	credentials    map[record.Digest]heldCredential
	revocations    map[record.Digest]store.CredRevocationRecord // by the digest of the revoked credential
	locallyRevoked map[ident.NodeID]store.LocalRevokedRecord
	peers          map[ident.NodeID]heldPeer

	// view is what trust.Evaluate reads, without the retired nodes, which
	// trustView adds from the revocation set at each use. It is rebuilt whole when
	// anything above changes and never changed in place, so a snapshot that holds
	// it keeps what it was given.
	view trust.View
}

// buildTrust verifies the trust and peer records of a scan into a state.
func (e *Engine) buildTrust(sc *store.Scan) *trustState {
	t := &trustState{
		credentials:    map[record.Digest]heldCredential{},
		revocations:    map[record.Digest]store.CredRevocationRecord{},
		locallyRevoked: map[ident.NodeID]store.LocalRevokedRecord{},
		peers:          map[ident.NodeID]heldPeer{},
	}
	for _, c := range sc.Credentials {
		h, err := e.verifyCredential(c.Record)
		if err != nil {
			e.log.Warn("a stored credential does not count", "credential", c.Digest.String(), "error", err.Error())
			continue
		}
		t.credentials[c.Digest] = heldCredential{rec: c, cred: h}
	}
	for _, r := range sc.CredRevocations {
		if _, err := e.verifyRevocation(r.Record); err != nil {
			e.log.Warn("a stored credential revocation does not count", "credential", r.Revoked.String(), "error", err.Error())
			continue
		}
		t.revocations[r.Revoked] = r
	}
	for _, l := range sc.LocalRevoked {
		t.locallyRevoked[l.Node] = l
	}
	for _, p := range sc.Peers {
		h, err := e.verifyPeer(p)
		if err != nil {
			e.log.Warn("a stored peer certificate does not count", "node", p.Node.String(), "error", err.Error())
			continue
		}
		t.peers[p.Node] = h
	}
	t.rebuild(e.d.Node)
	return t
}

// verifyCredential verifies a credential record and holds it to being one of
// this node's: only those are kept, and only those reach the trust view.
func (e *Engine) verifyCredential(rec record.Record) (record.Verified[credential.Credential], error) {
	v, err := record.Verify(credential.Descriptor, rec)
	if err != nil {
		return v, err
	}
	if c := v.Payload(); c.A != e.d.Node && c.B != e.d.Node {
		return v, errors.New("the credential is not one of this node's")
	}
	return v, nil
}

// verifyRevocation verifies the revocation of a credential of this node.
func (e *Engine) verifyRevocation(rec record.Record) (record.Verified[credential.Revocation], error) {
	v, err := record.Verify(credential.RevocationDescriptor, rec)
	if err != nil {
		return v, err
	}
	if c := v.Payload().Credential; c.A != e.d.Node && c.B != e.d.Node {
		return v, errors.New("the revocation is not of a credential of this node's")
	}
	return v, nil
}

// verifyPeer verifies the certificate a peer record holds.
func (e *Engine) verifyPeer(p store.PeerRecord) (heldPeer, error) {
	if p.Node == e.d.Node {
		return heldPeer{}, errors.New("a certificate recorded for this node itself")
	}
	cert, err := record.Verify(record.NetworkKeyCertificateDescriptor, p.Certificate)
	if err != nil {
		return heldPeer{}, err
	}
	return heldPeer{rec: p, cert: cert}, nil
}

// rebuild makes the view from the records, in an order that does not depend on the
// maps.
func (t *trustState) rebuild(local ident.NodeID) {
	v := trust.View{
		Local: local, Settings: trust.DefaultSettings(),
		LocallyRevoked:             trust.NodeSet{},
		RevokedCredentials:         trust.DigestSet{},
		DeclaredAcceptsInheritance: map[ident.NodeID]bool{},
	}
	for _, c := range t.credentials {
		v.Credentials = append(v.Credentials, credential.Facts(c.cred))
	}
	slices.SortFunc(v.Credentials, func(a, b trust.Credential) int { return bytes.Compare(a.Digest[:], b.Digest[:]) })
	for d := range t.revocations {
		v.RevokedCredentials[trust.CredentialDigest(d)] = struct{}{}
	}
	for n := range t.locallyRevoked {
		v.LocallyRevoked[n] = struct{}{}
	}
	t.view = v
}

// trustView is the view of the moment: the records above and the nodes the
// revocation set retires, which is the one source of retirement.
func (e *Engine) trustView() trust.View { return e.d.Config.RevocationSet().TrustView(e.trust.view) }

// trustOf is the trust of this node in a node elsewhere, at now.
func (e *Engine) trustOf(node ident.NodeID, now time.Time) trust.Outcome {
	return trust.Evaluate(e.trustView(), node, now).Outcome
}

// trustChanged is what every change of the trust records ends with: the view is
// made again from the records and the network is told. It is the one place either
// is done.
func (e *Engine) trustChanged() {
	e.trust.rebuild(e.d.Node)
	e.publishMembership()
}

// ---------------------------------------------------------------------------
// The certificates of the peers

// peerCertificate is a certificate a peer presented, with the record it was
// verified from and the serial state the caller saw it at.
type peerCertificate struct {
	cert record.Verified[record.NetworkKeyCertificate]
	rec  record.Record
	seen record.SerialSeen
}

// planPeer decides what a certificate of a peer changes in what is stored: the
// peer record to write, or nil when what is stored already says it. The decision
// is made against the state of the loop, which is the only state that is current:
// the network judged the certificate against the snapshot it was given, and
// several connections may have judged different certificates of one node since.
//
// The accepted certificate only moves forward, and so does the highest serial seen
// (record.DecideSerial): a certificate below the highest serial seen is an old
// key, and the same serial with other content is two certificates signed under one
// serial; both are refused, and the stored state is left as it was. The very
// certificate that was seen is the peer's reconnect and refuses nothing.
func (e *Engine) planPeer(pc peerCertificate) (*store.PeerRecord, error) {
	c := pc.cert.Payload()
	switch {
	case c.Node == e.d.Node:
		return nil, invalidArgument("certificate", errors.New("the certificate is this node's own"))
	case pc.rec.Digest() != pc.cert.Digest():
		return nil, invalidArgument("record", errors.New("the record is not the certificate's"))
	case pc.seen.Node != c.Node:
		return nil, invalidArgument("seen", errors.New("the serial state is of another node"))
	}
	held, have := e.trust.peers[c.Node]
	var stored *record.SerialSeen
	if have {
		s := held.seen()
		stored = &s
	}
	switch record.DecideSerial(stored, pc.cert) {
	case record.SerialAccept:
	case record.SerialStale:
		if c.Serial < stored.Serial {
			return nil, invalidArgument("certificate", fmt.Errorf("serial %d is below the highest seen, %d", c.Serial, stored.Serial))
		}
	case record.SerialConflict:
		e.log.Warn("a node presented another certificate under a serial it had issued: its identity key may be copied", "node", c.Node.String(), "serial", c.Serial)
		return nil, invalidArgument("certificate", fmt.Errorf("serial %d was seen with other content", c.Serial))
	default:
		return nil, invalidArgument("certificate", errors.New("the certificate cannot be judged against the serial seen"))
	}

	// The certificate is the highest seen from here on, and the serial state is the
	// highest of the three.
	next := record.SerialSeenOf(pc.cert)
	if pc.seen.Serial > next.Serial {
		next = pc.seen
	}
	if stored != nil && stored.Serial >= next.Serial {
		next = *stored
	}
	replaces := !have || c.Serial > held.cert.Payload().Serial
	advances := !have || next.Serial > stored.Serial
	if !replaces && !advances {
		return nil, nil
	}
	rec := store.PeerRecord{Node: c.Node, Certificate: pc.rec, SerialSeen: next.Serial, SeenDigest: next.Digest}
	if !replaces {
		rec.Certificate = held.rec.Certificate
	}
	return &rec, nil
}

// takePeer keeps a peer record that is on disk now.
func (e *Engine) takePeer(rec store.PeerRecord) error {
	h, err := e.verifyPeer(rec)
	if err != nil {
		return fmt.Errorf("engine: the peer record just written does not verify: %w", err)
	}
	e.trust.peers[rec.Node] = h
	return nil
}

type cmdNotePeer struct {
	call[struct{}]
	pc peerCertificate
}

func (c *cmdNotePeer) gate() gate { return gateLoopback }
func (c *cmdNotePeer) run(e *Engine, _ time.Time) {
	c.done(struct{}{}, e.classify(e.notePeer(c.pc)))
}

// NotePeerCertificate takes the certificate a member's exchange presented as the
// accepted certificate of its node, when its serial is above the one stored (or
// none is), and keeps the serial seen. The network layer calls it before the
// connection becomes a member's, so a peer whose certificate the engine did not
// take is not served. The caller has verified cert and decided its node is a
// member; rec is the record it was verified from, which is what is stored, as a
// verified certificate carries no signature. It runs in the loop and waits for it.
//
// A certificate of this node itself, one below the highest serial seen for its
// node and one that has the serial of another certificate seen are refused.
func (e *Engine) NotePeerCertificate(ctx context.Context, cert record.Verified[record.NetworkKeyCertificate], rec record.Record, seen record.SerialSeen) error {
	c := &cmdNotePeer{call: newCall[struct{}](), pc: peerCertificate{cert: cert, rec: rec, seen: seen}}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

// RefusalReason is the name of the refusal reason in err's chain, and false when
// err carries none: how a call from another node is answered.
func (e *Engine) RefusalReason(err error) (string, bool) {
	r, ok := ReasonOf(err)
	if !ok {
		return "", false
	}
	return r.String(), true
}

// notePeer is NotePeerCertificate in the loop.
func (e *Engine) notePeer(pc peerCertificate) error {
	rec, err := e.planPeer(pc)
	if err != nil || rec == nil {
		return err
	}
	if err := e.d.Store.PeerCert(*rec); err != nil {
		return err
	}
	if err := e.takePeer(*rec); err != nil {
		return err
	}
	e.trustChanged()
	return nil
}

// ---------------------------------------------------------------------------
// Credentials, their revocations and local revocation. These are the loop's
// steps of the pairing and of the owner's operations on trust; the commands that
// carry them come with those.

// installCredential keeps a credential this node holds and, when the call has it,
// the certificate of the peer it was made with, in one transaction. The credential
// is the commit point: a peer certificate that did not follow it comes again with
// the peer's next exchange.
func (e *Engine) installCredential(rec record.Record, peer *peerCertificate) error {
	cred, err := e.verifyCredential(rec)
	if err != nil {
		return invalidArgument("credential", err)
	}
	stored := store.CredentialRecord{Digest: cred.Digest(), Record: rec}
	var peerRec *store.PeerRecord
	if peer != nil {
		c := cred.Payload()
		if node := peer.cert.Payload().Node; node != c.A && node != c.B {
			return invalidArgument("certificate", errors.New("the certificate is not of the other end of the credential"))
		}
		if peerRec, err = e.planPeer(*peer); err != nil {
			return err
		}
	}
	if err := e.d.Store.TrustCredential(stored, peerRec); err != nil {
		return err
	}
	e.trust.credentials[stored.Digest] = heldCredential{rec: stored, cred: cred}
	if peerRec != nil {
		if err := e.takePeer(*peerRec); err != nil {
			return err
		}
	}
	e.trustChanged()
	return nil
}

// installRevocation keeps the revocation of a credential of this node, made by
// either end of it. A revocation is permanent: nothing takes it back.
func (e *Engine) installRevocation(rec record.Record) error {
	rev, err := e.verifyRevocation(rec)
	if err != nil {
		return invalidArgument("revocation", err)
	}
	stored := store.CredRevocationRecord{Revoked: record.Digest(rev.Payload().Digest()), Record: rec}
	if err := e.d.Store.TrustRevocation(stored); err != nil {
		return err
	}
	e.trust.revocations[stored.Revoked] = stored
	e.trustChanged()
	return nil
}

// revokeCredential revokes a credential this node holds, signing with its identity
// key. A credential that is not held, or is revoked already, is refused.
func (e *Engine) revokeCredential(digest record.Digest) error {
	h, ok := e.trust.credentials[digest]
	if !ok {
		return invalidArgument("credential", errors.New("this node holds no such credential"))
	}
	if _, done := e.trust.revocations[digest]; done {
		return invalidArgument("credential", errors.New("the credential is revoked already"))
	}
	rec, err := e.signRevocation(h)
	if err != nil {
		return err
	}
	return e.installRevocation(rec)
}

// signRevocation is the revocation of a held credential, signed by this node's
// identity key. Nothing is signed in the name of an identity that was retired.
func (e *Engine) signRevocation(h heldCredential) (record.Record, error) {
	if e.d.Keys == nil {
		return record.Record{}, errNoKeys
	}
	if e.retired {
		return record.Record{}, refuse(ReasonIdentityRetired)
	}
	rec, err := credential.IssueRevocation(h.cred.Payload(), e.d.Keys.IdentitySigner())
	if err != nil {
		return record.Record{}, fmt.Errorf("engine: revoking credential %s: %w", h.rec.Digest, err)
	}
	return rec, nil
}

// revokeNodeLocally removes a node from this node's trust, as the operation named
// x.name: the node is refused whatever path leads to it, and the credentials this
// node holds with it are revoked with its identity key, which tells the other end,
// and the nodes that reach it through this one, that the edge is gone. The entry,
// the revocations and the result are one transaction (declTrustLocalRevoke); done
// again for a node already removed it writes only what is missing.
func (e *Engine) revokeNodeLocally(x *opRun, node ident.NodeID, encode func(NodeRemoved) ([]byte, error)) ([]byte, error) {
	effects, err := trust.RevokeLocally(e.trust.view, node)
	if err != nil {
		return nil, invalidArgument("node", err)
	}
	entry := store.LocalRevokedRecord{Node: node, At: x.now.UTC(), Operation: x.name}
	old, already := e.trust.locallyRevoked[node]
	if already {
		entry = old // the first removal is the one the owner is told of
	}
	pl, err := store.PrepareLocalRevoked(entry)
	if err != nil {
		return nil, err
	}
	out := NodeRemoved{Node: node, Already: already}
	var revoked []store.CredRevocationRecord
	var prepared []store.PreparedCredRevocation
	for _, d := range effects.RevokeCredentials {
		digest := record.Digest(d)
		rec, err := e.signRevocation(e.trust.credentials[digest])
		if err != nil {
			return nil, err
		}
		r := store.CredRevocationRecord{Revoked: digest, Record: rec}
		p, err := store.PrepareCredRevocation(r)
		if err != nil {
			return nil, err
		}
		revoked, prepared = append(revoked, r), append(prepared, p)
		out.Credentials = append(out.Credentials, digest)
	}
	result, err := encode(out)
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	pr, err := store.PrepareAdminResult(x.record(result))
	if err != nil {
		return nil, err
	}
	tx := e.d.Writer.Begin(&declTrustLocalRevoke)
	defer tx.Close()
	if err := store.PutLocalRevoked(tx, store.RoleLocalRevoked, pl); err != nil {
		return nil, err
	}
	for _, p := range prepared {
		if err := store.PutCredRevocation(tx, store.RoleCredRevocation, p); err != nil {
			return nil, err
		}
	}
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	e.trust.locallyRevoked[node] = entry
	for _, r := range revoked {
		e.trust.revocations[r.Revoked] = r
	}
	e.trustChanged()
	return result, nil
}

// clearLocallyRevoked takes a node back into this node's trust, as the operation
// named x.name. It restores only what the removal did not end: the credentials
// revoked with it stay revoked, so a node that held one is trusted again only
// through a new pairing, which the result says. The removal of the entry and the
// result are one transaction (declTrustClear); a node that was not removed changes
// nothing and stores its result alone.
func (e *Engine) clearLocallyRevoked(x *opRun, node ident.NodeID, encode func(NodeCleared) ([]byte, error)) ([]byte, error) {
	_, removed := e.trust.locallyRevoked[node]
	result, err := encode(NodeCleared{Node: node, NewPairingRequired: removed && e.holdsRevokedCredential(node)})
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	if !removed {
		return result, e.storeResultOnly(x, result)
	}
	pr, err := store.PrepareAdminResult(x.record(result))
	if err != nil {
		return nil, err
	}
	tx := e.d.Writer.Begin(&declTrustClear)
	defer tx.Close()
	if err := store.RemoveLocalRevoked(tx, store.RoleLocalRevoked, store.RoleLocalRevokedDir, node); err != nil {
		return nil, err
	}
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	delete(e.trust.locallyRevoked, node)
	e.trustChanged()
	return result, nil
}
