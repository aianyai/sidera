package store

import (
	"slices"
	"sort"

	"github.com/aianyai/sidera/internal/ident"
)

type seqPair struct {
	sender ident.EndpointID
	target ident.Address
}

// SeqBook holds, for each pair of sending endpoint and target address, the
// highest sequence number used. The next number of a pair is that plus one.
// It is built from a Scan: the highest of the floor of the pair and the numbers
// of the regular source records that are still there.
//
// It is not safe for concurrent use. The engine's single writer loop owns it.
type SeqBook struct{ high map[seqPair]uint64 }

// NewSeqBook builds the book from a scan.
func NewSeqBook(sc *Scan) *SeqBook {
	b := &SeqBook{high: map[seqPair]uint64{}}
	for _, f := range sc.Seq {
		for to, n := range f.Floors {
			b.Note(f.Sender, to, n)
		}
	}
	for _, o := range sc.Out {
		for _, to := range o.Key.Recipients {
			if n, ok := o.number(to); ok {
				b.Note(o.Key.Sender, to, n)
			}
		}
	}
	return b
}

// number is the sequence number a regular source record used for the target to.
// A lost record has none: its numbers are lost, and the floor of its pair was
// raised above them when it was quarantined.
func (o OutRecord) number(to ident.Address) (uint64, bool) {
	if o.Lost != nil || !slices.Contains(o.Key.Recipients, to) {
		return 0, false
	}
	n, ok := o.Envelope.Seq[to.Endpoint]
	return n, ok
}

// senderFloors is the floor of every target of one sending endpoint.
type senderFloors struct {
	Sender ident.EndpointID
	Floors map[ident.Address]uint64
}

// floorsOf is what a sweep must remember before it removes the source records
// outs: for every sending endpoint and every target of their keys, the highest
// number they used. It is read from the records themselves, so it cannot be
// short of a number the removal is about to lose, however the caller kept its
// view. Senders come in a fixed order.
func floorsOf(outs []OutRecord) []senderFloors {
	by := map[ident.EndpointID]map[ident.Address]uint64{}
	for _, o := range outs {
		for _, to := range o.Key.Recipients {
			if n, ok := o.number(to); ok && n > 0 {
				if by[o.Key.Sender] == nil {
					by[o.Key.Sender] = map[ident.Address]uint64{}
				}
				by[o.Key.Sender][to] = max(by[o.Key.Sender][to], n)
			}
		}
	}
	out := make([]senderFloors, 0, len(by))
	for sender, f := range by {
		out = append(out, senderFloors{Sender: sender, Floors: f})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sender.String() < out[j].Sender.String() })
	return out
}

// Next is the number to give the next message from sender to target.
func (b *SeqBook) Next(sender ident.EndpointID, target ident.Address) uint64 {
	return b.High(sender, target) + 1
}

// High is the highest number used for the pair, 0 if none.
func (b *SeqBook) High(sender ident.EndpointID, target ident.Address) uint64 {
	return b.high[seqPair{sender, target}]
}

// Note records that seq was used for the pair. Call it after the transaction
// that wrote it has committed, never before: a number whose write failed was
// never sent and is given again.
func (b *SeqBook) Note(sender ident.EndpointID, target ident.Address, seq uint64) {
	p := seqPair{sender, target}
	if seq > b.high[p] {
		b.high[p] = seq
	}
}
