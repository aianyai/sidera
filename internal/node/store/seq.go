package store

import (
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
		if o.Lost != nil {
			continue
		}
		for _, to := range o.Key.Recipients {
			if n, ok := o.Envelope.Seq[to.Endpoint]; ok {
				b.Note(o.Key.Sender, to, n)
			}
		}
	}
	return b
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

// FloorWrites returns the floors to write before the given source records are
// removed: for every sender and every target of their keys, the highest number
// the book holds.
func (b *SeqBook) FloorWrites(outs []OutRecord) []FloorWrite {
	by := map[ident.EndpointID]map[ident.Address]uint64{}
	for _, o := range outs {
		for _, to := range o.Key.Recipients {
			if n := b.High(o.Key.Sender, to); n > 0 {
				if by[o.Key.Sender] == nil {
					by[o.Key.Sender] = map[ident.Address]uint64{}
				}
				by[o.Key.Sender][to] = n
			}
		}
	}
	var out []FloorWrite
	for s, f := range by {
		out = append(out, FloorWrite{Sender: s, Floors: f})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sender.String() < out[j].Sender.String() })
	return out
}
