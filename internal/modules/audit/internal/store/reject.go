package store

import "context"

// Reject keeps a message the writer cannot chain, so that it is never
// silently dropped. Rejecting the same stream message twice is a no-op.
func Reject(ctx context.Context, q Querier, messageID, subject string, streamSeq uint64, payload []byte, reason string) error {
	if _, err := q.Exec(ctx, `INSERT INTO audit.rejected_messages (message_id, subject, stream_seq, payload, reason)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
		messageID, subject, int64(min(streamSeq, MaxSequence)), payload, reason); err != nil {
		return wrapDB(err, "keep the rejected message")
	}
	return nil
}
