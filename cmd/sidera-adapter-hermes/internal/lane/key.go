// Package lane holds the decisions of the Hermes lane: which endpoint a card uses,
// which card a received message answers, what waits behind what, what to do with a
// card that comes back, when a remote session is closed. Every function is pure:
// it reads the records of the state package and the time it is given and returns
// a verdict. Doing what the verdict says, and writing the state transition before
// it, is the caller's.
package lane

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// OrphanKey is the one shared endpoint of cards that come from no conversation
// and no cron job, and of cards that could not get an endpoint of their own.
const OrphanKey = "orphan"

// Source is where a card came from, as far as the board says.
type Source = state.Source

// Kind tells how an endpoint, and so the remote sessions it created, is used.
type Kind int

const (
	// KindConversation: a chat; the same conversation sends follow-ups.
	KindConversation Kind = iota
	// KindCron: a scheduled job; every run is a new session without memory.
	KindCron
	// KindOrphan: the shared endpoint.
	KindOrphan
)

// hashLen is the length of the digest that ends a shortened key.
const hashLen = 32

// EndpointKey returns the key of the conversation endpoint a card sends from:
// one per chat (and thread), one per cron job, otherwise the shared orphan.
// A card does not get a key of its own: an endpoint id is never released, so
// that would grow the node's configuration without bound, while conversations
// and jobs grow far more slowly.
//
// Segments are escaped so that two different sources never share a key; a chat id
// can hold colons (a terminal session's id is a whole session key). A key longer
// than the protocol allows is cut and ended with a digest of the whole key, so the
// same source always gets the same key.
func EndpointKey(s Source) string {
	var key string
	switch {
	case s.Platform != "" && s.ChatID != "":
		parts := []string{"chat", escape(s.Profile), escape(s.Platform), escape(s.ChatID)}
		if s.ThreadID != "" {
			parts = append(parts, escape(s.ThreadID))
		}
		key = strings.Join(parts, ":")
	case s.CronJobID != "":
		key = strings.Join([]string{"cron", escape(s.Profile), escape(s.CronJobID)}, ":")
	default:
		return OrphanKey
	}
	if utf8.RuneCountInString(key) <= adapterproto.MaxIdentLen {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	keep := adapterproto.MaxIdentLen - 1 - hashLen
	return string([]rune(key)[:keep]) + "~" + hex.EncodeToString(sum[:])[:hashLen]
}

func escape(s string) string {
	return strings.NewReplacer("%", "%25", ":", "%3A").Replace(s)
}

// KindOf tells the kind of endpoint from its key.
func KindOf(key string) Kind {
	switch {
	case key == OrphanKey:
		return KindOrphan
	case strings.HasPrefix(key, "cron:"):
		return KindCron
	default:
		return KindConversation
	}
}
