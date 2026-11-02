// Package attached knows how a Claude Code session that a person runs shows
// itself to the adapter: the registry file the runtime keeps for each running
// session, the line its inbox socket takes, and the turns it goes through. It
// is pure: the caller reads the files and the clock and hands over the bytes.
//
// The registry file and the socket line are not documented by Claude Code, and
// this package is the one place that relies on their form. A change of form
// shows as an error here (ErrRegistryForm), never as a guess.
package attached
