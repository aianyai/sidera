package store

import "github.com/aianyai/sidera/internal/ident"

// SeqBook holds, for each pair of sending endpoint and target address, the
// highest sequence number used. The next number of a pair is that plus one.
// It is built from a Scan: the highest of the floor of the pair and the numbers
// of the regular source records that are still there.
//
// It is not safe for concurrent use. The engine's single writer loop owns it.
type SeqBook struct{}

// NewSeqBook builds the book from a scan.
func NewSeqBook(sc *Scan) *SeqBook { panic(errNotImplemented) }

// Next is the number to give the next message from sender to target.
func (b *SeqBook) Next(sender ident.EndpointID, target ident.Address) uint64 {
	panic(errNotImplemented)
}

// High is the highest number used for the pair, 0 if none.
func (b *SeqBook) High(sender ident.EndpointID, target ident.Address) uint64 {
	panic(errNotImplemented)
}

// Note records that seq was used for the pair. Call it after the transaction
// that wrote it has committed, never before: a number whose write failed was
// never sent and is given again.
func (b *SeqBook) Note(sender ident.EndpointID, target ident.Address, seq uint64) {
	panic(errNotImplemented)
}

// FloorWrites returns the floors to write before the given source records are
// removed: for every sender and every target of their keys, the highest number
// the book holds.
func (b *SeqBook) FloorWrites(outs []OutRecord) []FloorWrite { panic(errNotImplemented) }
