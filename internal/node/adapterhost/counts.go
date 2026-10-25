package adapterhost

// ConnCounts is what the host counts of the connection of one instance, for the
// caller that asks whether the system is quiet.
type ConnCounts struct {
	// Connected is whether a connection past adapter.start exists.
	Connected bool
	// Handled is how many notifications of the connection the engine has finished
	// with, and Buffered how many the host has received and the engine has not
	// finished with. The adapter says how many it sent; the system is quiet only
	// when Handled equals that and Buffered is zero.
	Handled  int
	Buffered int
	// Requests is how many requests of the adapter are being answered now.
	Requests int
	// Calls lists the methods of the requests the host has sent the adapter and has
	// no answer to, one entry per request, in no particular order. A ping is not
	// among them: it only asks whether the adapter is there, and an adapter that
	// holds a request on purpose is not hanging.
	Calls []string
	// Working is whether the host is in the middle of something it does on its
	// own: handling the end of a connection, starting an instance, taking one on,
	// answering a timer. It is also true while a timer of the host is due and
	// not yet taken, which is how a caller that moved the test clock finds out
	// that the host has not yet reacted to it.
	Working bool
}
