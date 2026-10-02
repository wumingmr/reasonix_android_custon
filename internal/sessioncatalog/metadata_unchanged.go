package sessioncatalog

import (
	"context"
	"database/sql"
	"errors"
)

// Discovery has already read bounded source metadata. Compare every projected
// field, not just size/mtime: this shortcut never certifies transcript content.
func sameMetadataProjection(existing, incoming SessionRecord) bool {
	return existing.MissingSince == 0 && sameSessionIndexInput(existing, incoming) &&
		existing.RecoveryCopy == incoming.RecoveryCopy &&
		existing.RecoveryGroupID == incoming.RecoveryGroupID &&
		existing.RecoveryRole == incoming.RecoveryRole &&
		existing.RecoveryCanonical == incoming.RecoveryCanonical &&
		existing.LogicalTopicID == incoming.LogicalTopicID &&
		existing.OrdinaryVisible == incoming.OrdinaryVisible &&
		existing.LogFormat == incoming.LogFormat && existing.HeadCount == incoming.HeadCount &&
		existing.SelectedHeadID == incoming.SelectedHeadID
}

func refreshUnchangedMetadata(ctx context.Context, tx *sql.Tx, incoming SessionRecord, pathKey string, generation int64) (bool, error) {
	// Recheck at the publication boundary; a pre-transaction observation alone
	// cannot authorize skipping a concurrent metadata update.
	current, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionSelectColumns+` FROM catalog_sessions WHERE path_key=?`, pathKey))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || !sameMetadataProjection(current, incoming) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE catalog_sessions SET seen_generation=MAX(seen_generation,?) WHERE path_key=?`, generation, pathKey)
	return err == nil, err
}
