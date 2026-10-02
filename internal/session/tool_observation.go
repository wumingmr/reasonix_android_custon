package session

import (
	"context"
	"database/sql"
	"reasonix/internal/sessioncontent"
)

// ToolObservation separates execution evidence from resident result bodies.
type ToolObservation struct {
	State      string              `json:"state"`
	MessageID  string              `json:"messageId,omitempty"`
	Version    int                 `json:"version,omitempty"`
	ContentRef *sessioncontent.Ref `json:"contentRef,omitempty"`
}

func (q *Query) attachToolObservations(ctx context.Context, db *sql.DB, ref SessionRef, page *HistoryWindowPage) error {
	for i := range page.Messages {
		message := &page.Messages[i]
		if message.Role != "assistant" {
			continue
		}
		rows, err := db.QueryContext(ctx, `SELECT calls.call_id,COALESCE(CASE WHEN result.message_id IS NOT NULL THEN results.state END,(SELECT state FROM tool_states WHERE call_id=calls.call_id AND sequence<=? ORDER BY sequence DESC LIMIT 1),'unknown'),COALESCE(result.message_id,''),COALESCE(result.version,0),COALESCE(result.content_digest,''),COALESCE(result.content_bytes,0),COALESCE(result.content_index_digest,'')
   FROM messages owner JOIN tool_links calls ON calls.message_id=owner.message_id AND calls.digest=owner.content_digest AND calls.is_result=0
   LEFT JOIN tool_links results ON results.call_id=calls.call_id AND results.is_result=1
   LEFT JOIN messages result ON result.message_id=results.message_id AND result.content_digest=results.digest AND result.event_sequence<=? AND (result.valid_to=0 OR result.valid_to>?)
   WHERE owner.message_id=? AND owner.version=? ORDER BY result.event_sequence DESC`, page.SnapshotSequence, page.SnapshotSequence, page.SnapshotSequence, message.MessageID, message.Version)
		if err != nil {
			return err
		}
		observations := map[string]ToolObservation{}
		for rows.Next() {
			var id, digest, index string
			var size int64
			var observation ToolObservation
			if err = rows.Scan(&id, &observation.State, &observation.MessageID, &observation.Version, &digest, &size, &index); err != nil {
				rows.Close()
				return err
			}
			if prior, ok := observations[id]; ok && prior.MessageID != "" {
				continue
			}
			if observation.MessageID != "" && digest != "" {
				observation.ContentRef = &sessioncontent.Ref{Digest: digest, Bytes: size, IndexDigest: index, IntegrityBlock: sessioncontent.IntegrityBlockBytes, MediaType: "application/json"}
				q.authorizeContentForGeneration(ref.SessionID, q.storageGeneration(ref.SessionID), digest, size, index)
			}
			observations[id] = observation
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(observations) > 0 {
			message.ToolObservations = observations
		}
	}
	return nil
}
