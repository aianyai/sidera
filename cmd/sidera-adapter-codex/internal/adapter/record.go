package adapter

import (
	"context"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Paging of a read of a thread's record.
const (
	recordPage     = 20
	recordMaxPages = 50
)

// readTurns reads a thread's turns, newest first, with their items, page by page
// until stop says enough or the record ends. complete says the record was read to
// its first turn.
func readTurns(ctx context.Context, c *appserver.Client, threadID string, maxPages int, stop func(appserver.Turn) bool) (turns []appserver.Turn, complete bool, err error) {
	cursor := ""
	for page := 0; page < maxPages; page++ {
		res, err := c.ThreadTurnsList(ctx, appserver.ThreadTurnsListParams{
			ThreadID: threadID, Cursor: cursor, Limit: recordPage, ItemsView: "full", SortDirection: "desc",
		})
		if err != nil {
			return turns, false, err
		}
		for _, t := range res.Data {
			turns = append(turns, t)
			if stop != nil && stop(t) {
				return turns, false, nil
			}
		}
		if res.NextCursor == nil || *res.NextCursor == "" {
			return turns, true, nil
		}
		cursor = *res.NextCursor
	}
	return turns, false, nil
}

// hasPrompt reports whether the turn records the prompt of an attempt.
func hasPrompt(t appserver.Turn, attempt adapterproto.AttemptID) bool {
	for _, id := range t.UserMessageIDs() {
		if id == string(attempt) {
			return true
		}
	}
	return false
}

func turnState(status string) adapterproto.TurnState {
	switch status {
	case appserver.TurnCompleted:
		return adapterproto.TurnStateCompleted
	case appserver.TurnInterrupted:
		return adapterproto.TurnStateInterrupted
	case appserver.TurnFailed:
		return adapterproto.TurnStateFailed
	}
	return adapterproto.TurnStateRunning
}
