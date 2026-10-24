package delivery

import "time"

// NextTick is not implemented yet.
func (r Record) NextTick() time.Time { return time.Time{} }

// NextFlush is not implemented yet.
func (q *FeedbackQueue) NextFlush(p FeedbackParams) (time.Time, error) {
	return time.Time{}, nil
}
