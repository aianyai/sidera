package keys

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// Initialized reports whether init has finished: keys/init.rec exists and is
// valid. A marker that is damaged or that anyone but the owner can reach is an
// error, not an answer.
func Initialized(r fsx.Reader) (bool, error) {
	data, err := readOwnerOnly(r, InitMarkerPath)
	if errors.Is(err, ErrMissing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := decodeInit(data); err != nil {
		return false, corrupt(InitMarkerPath, err)
	}
	return true, nil
}

// InitPlan is what the key side of init has decided to write, after reading what
// is already there. It holds key material and prints as an address.
type InitPlan struct{ p *initPlan }

type initPlan struct {
	node    ident.NodeID
	writes  []plannedWrite
	marker  []byte
	selfRev SelfRevocation
}

type plannedWrite struct {
	role, path string
	data       []byte
}

func (p *initPlan) add(role, path string, data []byte) {
	p.writes = append(p.writes, plannedWrite{role: role, path: path, data: data})
}

// Node is the node that init sets up: the node of the identity key, new or
// already there.
func (p *InitPlan) Node() ident.NodeID { return p.p.node }

// SelfRevocation is the self-revocation of the node, new or already there, for
// the instructions init prints: where it is and that it is to be exported and
// kept off the machine.
func (p *InitPlan) SelfRevocation() SelfRevocation { return p.p.selfRev }

// WriteKeys makes the writes of the plan that belong to keys/, in the order
// InitWrites declares: only those the plan needs, the files that were already
// valid are not written. It is called in the init transaction of the composing
// package, before the writes of the other owners.
func (p *InitPlan) WriteKeys(tx *txn.Tx) error {
	for _, w := range p.p.writes {
		if err := tx.Replace(w.role, w.path, w.data, fsx.ModePrivate); err != nil {
			return err
		}
	}
	return nil
}

// WriteMarker makes the last write of init, the marker. It is called after the
// writes of every owner.
func (p *InitPlan) WriteMarker(tx *txn.Tx) error {
	return tx.Replace(RoleInitMarker, InitMarkerPath, p.p.marker, fsx.ModePrivate)
}

// PlanInit reads keys/ and plans the key side of init. It writes nothing. The
// rule is to look before writing and never to overwrite: for every file that
// exists, a valid one is reused and an invalid one is refused, naming it, so the
// owner decides; nothing is repaired behind the owner's back.
//
//   - A key file is valid if it parses. Keys that are missing are generated from
//     rd, which is the only source of randomness, 32 bytes for each.
//   - The self-revocation is valid only if it verifies under the identity key
//     that is current now: a statement of another identity, left behind when the
//     owner deleted the keys, is refused and never reused or replaced.
//   - The clock record is reused whatever it says, the pause flag and a mark
//     ahead of the clock included: deleting init.rec to run init again must not
//     be a way around a rollback pause.
//   - The serial file is reused if it parses and is this node's.
//   - The certificate is the one thing that may be replaced, because it can be
//     issued again. It is kept if it is valid, names the network key and is the
//     one the serial file names. Otherwise a new one is signed at a serial above
//     every serial known (the serial file's, and a valid certificate's of this
//     node), and the serial file is written first. A missing serial file next to
//     a valid certificate is written from the certificate.
//
// A time that the files cannot hold (the zero time, one before
// 1970-01-01T00:00:01Z or after year 9999) is a plain error of the caller, before
// anything is read or drawn. Init that finished is refused with ErrAlreadyInitialized. A file that anyone
// but the owner can reach is refused, naming it, before anything is read from it.
func PlanInit(r fsx.Reader, rd io.Reader, now time.Time) (*InitPlan, error) {
	if err := checkRecordTime(now); err != nil {
		return nil, fmt.Errorf("keys: init needs the current time: %w", err)
	}
	if done, err := Initialized(r); err != nil {
		return nil, err
	} else if done {
		return nil, fileError(InitMarkerPath, ErrAlreadyInitialized, "")
	}
	if err := CheckPermissions(r); err != nil {
		return nil, err
	}

	p := &initPlan{}
	identity, err := readOrGenerate(r, rd, p, RoleIdentityKey, IdentityKeyPath, kindIdentityKey)
	if err != nil {
		return nil, err
	}
	network, err := readOrGenerate(r, rd, p, RoleNetworkKey, NetworkKeyPath, kindNetworkKey)
	if err != nil {
		return nil, err
	}
	p.node = identity.node

	if err := planCertificate(r, p, identity, network, now); err != nil {
		return nil, err
	}
	if err := planSelfRevocation(r, p, identity); err != nil {
		return nil, err
	}
	if err := planClock(r, p, now); err != nil {
		return nil, err
	}
	p.marker = encodeInit(now)
	return &InitPlan{p: p}, nil
}

// readOrGenerate returns the key of the file, or a new one drawn from rd and
// added to the plan if the file is not there.
func readOrGenerate(r fsx.Reader, rd io.Reader, p *initPlan, role, path, kind string) (*privateKey, error) {
	k, err := readKey(r, path, kind)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, ErrMissing) {
		return nil, err
	}
	if k, err = generateKey(rd); err != nil {
		return nil, fmt.Errorf("keys: generating %s: %w", path, err)
	}
	p.add(role, path, encodeKeyFile(kind, k.seed()))
	return k, nil
}

// planCertificate decides the serial file and the certificate together.
func planCertificate(r fsx.Reader, p *initPlan, identity, network *privateKey, now time.Time) error {
	seen, haveSeen, err := readSerialIfPresent(r)
	if err != nil {
		return err
	}
	if haveSeen && seen.Node != identity.node {
		return fileError(SerialPath, ErrInconsistent, "it is not the serial of this node")
	}

	// A certificate that exists is looked at without refusing it: it is replaced
	// when it does not fit. Its serial still counts when it is this node's, so
	// that the new serial is above it.
	var current certificate
	haveCert, ours, fits := false, false, false
	if data, err := readOwnerOnly(r, CertificatePath); err == nil {
		if c, err := verifyCertificate(data); err == nil {
			current, haveCert = c, true
			pl := c.verified.Payload()
			ours = pl.Node == identity.node
			fits = ours && pl.NetworkKey == network.pub
		}
	} else if !errors.Is(err, ErrMissing) {
		return err
	}

	switch {
	case haveSeen && fits && seen == record.SerialSeenOf(current.verified):
		return nil // the certificate and the serial file agree
	case !haveSeen && fits:
		p.add(RoleSerial, SerialPath, encodeSerial(record.SerialSeenOf(current.verified)))
		return nil
	}

	var known uint64
	if haveSeen {
		known = seen.Serial
	}
	if haveCert && ours {
		known = max(known, current.verified.Payload().Serial)
	}
	issued, err := issueCertificate(identity, network, known+1, now)
	if err != nil {
		return err
	}
	p.add(RoleSerial, SerialPath, encodeSerial(record.SerialSeenOf(issued.verified)))
	p.add(RoleCertificate, CertificatePath, issued.wire)
	return nil
}

func readSerialIfPresent(r fsx.Reader) (record.SerialSeen, bool, error) {
	seen, err := readSerial(r)
	if errors.Is(err, ErrMissing) {
		return record.SerialSeen{}, false, nil
	}
	return seen, err == nil, err
}

// planSelfRevocation reuses the statement on disk if it verifies under the
// current identity key, refuses it if not, and issues one if there is none.
func planSelfRevocation(r fsx.Reader, p *initPlan, identity *privateKey) error {
	sr, err := ReadSelfRevocation(r)
	switch {
	case err == nil:
		if sr.node != identity.node {
			return fileError(SelfRevocationPath, ErrInconsistent, "it is not the statement of the identity key")
		}
		p.selfRev = sr
		return nil
	case !errors.Is(err, ErrMissing):
		return err
	}
	rec, err := revocation.IssueNodeSelfRevocation(identity.signer())
	if err != nil {
		return fmt.Errorf("keys: issuing the self-revocation: %w", err)
	}
	wire, err := rec.Encode()
	if err != nil {
		return fmt.Errorf("keys: issuing the self-revocation: %w", err)
	}
	sr, err = verifySelfRevocation(wire)
	if err != nil {
		return fmt.Errorf("keys: the self-revocation issued does not verify: %w", err)
	}
	p.add(RoleSelfRevocation, SelfRevocationPath, wire)
	p.selfRev = sr
	return nil
}

// planClock keeps a clock record that is there and starts one at now otherwise.
func planClock(r fsx.Reader, p *initPlan, now time.Time) error {
	data, err := readOwnerOnly(r, ClockPath)
	switch {
	case err == nil:
		if _, err := decodeClock(data); err != nil {
			return corrupt(ClockPath, err)
		}
		return nil
	case !errors.Is(err, ErrMissing):
		return err
	}
	p.add(RoleClock, ClockPath, encodeClock(ClockState{HighWater: now}))
	return nil
}
