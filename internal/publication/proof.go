package publication

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// ReachabilityProofType is the type string of an administrator reachability
// proof.
const ReachabilityProofType = "sidera/reachability-proof/1"

// ReachabilityProof is the payload of a proof a node gives to a person on its
// authorized list. The person shows it to any member of the cluster to ask for
// the node's reachability record. It grants no management right: that stays with
// the node's own list. The node's identity key signs it, because the person is
// not a node and has no trust edge to show.
type ReachabilityProof struct {
	Person ident.PersonID
	Node   ident.NodeID
	// NotAfter is when the proof stops being valid. It is passed on as a fact:
	// admin.CheckReachabilityProof judges it.
	NotAfter time.Time
}

type reachabilityProofWire struct {
	Person   []byte `cbor:"1,keyasint"`
	Node     []byte `cbor:"2,keyasint"`
	NotAfter int64  `cbor:"3,keyasint"`
}

// ReachabilityProofDescriptor describes the proof record. It is signed by one
// key: the identity key of the node it names.
var ReachabilityProofDescriptor = record.NewDescriptor(ReachabilityProofType,
	proofToWire,
	proofFromWire,
	func(p ReachabilityProof) []record.SignerSlot {
		return []record.SignerSlot{record.NodeIdentityKey(p.Node)}
	},
)

func proofToWire(p ReachabilityProof) (reachabilityProofWire, error) {
	notAfter, err := record.TimeToWire(p.NotAfter)
	if err != nil {
		return reachabilityProofWire{}, fmt.Errorf("not_after: %w", err)
	}
	return reachabilityProofWire{Person: p.Person.Bytes(), Node: p.Node.Bytes(), NotAfter: notAfter}, nil
}

func proofFromWire(w reachabilityProofWire) (ReachabilityProof, error) {
	person, err := ident.PersonIDFromBytes(w.Person)
	if err != nil {
		return ReachabilityProof{}, fmt.Errorf("person: %w", err)
	}
	node, err := ident.NodeIDFromBytes(w.Node)
	if err != nil {
		return ReachabilityProof{}, fmt.Errorf("node: %w", err)
	}
	notAfter, err := record.TimeFromWire(w.NotAfter)
	if err != nil {
		return ReachabilityProof{}, fmt.Errorf("not_after: %w", err)
	}
	return ReachabilityProof{Person: person, Node: node, NotAfter: notAfter}, nil
}

// Admin returns the facts package admin reads. retirement is the result of the
// caller's lookup of the issuing node (p.Node) in the retirement records
// (revocation.Set.NodeRetirement): a proof of a retired node is void, and a
// lookup nobody made is refused.
func (p ReachabilityProof) Admin(retirement admin.RetirementStatus) admin.ReachabilityProof {
	return admin.ReachabilityProof{Node: p.Node, Person: p.Person, NotAfter: p.NotAfter, IssuerRetirement: retirement}
}
