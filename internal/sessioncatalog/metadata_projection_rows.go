package sessioncatalog

import (
	"context"
	"database/sql"
	"errors"

	"reasonix/internal/historywork"
)

func (c *Catalog) projectRegistryRow(ctx context.Context, tx *sql.Tx, p ProjectRecord, rootKey string) (bool, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO catalog_projects(
 scope,workspace_root,workspace_root_key,title,color,pinned,sort_order,updated_at)
 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(scope,workspace_root_key) DO UPDATE SET
 workspace_root=excluded.workspace_root,title=excluded.title,color=excluded.color,
 pinned=excluded.pinned,sort_order=excluded.sort_order,updated_at=excluded.updated_at
 WHERE workspace_root<>excluded.workspace_root OR title<>excluded.title OR color<>excluded.color
 OR pinned<>excluded.pinned OR sort_order<>excluded.sort_order`,
		p.Scope, p.WorkspaceRoot, rootKey, p.Title, p.Color, p.Pinned, p.SortOrder, c.opts.Now().UnixMilli())
	return registryMutationChanged(result, err)
}

func (c *Catalog) topicRegistryRow(ctx context.Context, tx *sql.Tx, topic TopicMetadata, rootKey string) (bool, error) {
	// Compare the actual projection, not a remembered input fingerprint: old
	// writers and source mutations also own these cache rows. Empty titles and
	// unspecified creation times retain the existing upsert semantics.
	var same bool
	err := tx.QueryRowContext(ctx, `SELECT metadata_present=1 AND workspace_root=?
 AND title=CASE WHEN ?<>'' THEN ? ELSE COALESCE(NULLIF((
 SELECT s.topic_title FROM catalog_sessions s WHERE s.scope=catalog_topics.scope
 AND s.workspace_root_key=catalog_topics.workspace_root_key AND s.topic_id=catalog_topics.topic_id
 ORDER BY s.recovery_copy ASC,s.last_activity_at DESC,s.path ASC LIMIT 1),''),title) END
 AND title_source=? AND pinned=? AND sort_order=? AND (?=0 OR created_at=?)
 FROM catalog_topics WHERE scope=? AND workspace_root_key=? AND topic_id=?`,
		topic.WorkspaceRoot, topic.Title, topic.Title, topic.TitleSource, topic.Pinned, topic.SortOrder,
		topic.CreatedAt, topic.CreatedAt, topic.Scope, rootKey, topic.TopicID).Scan(&same)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if same {
		return false, nil
	}
	return true, c.upsertTopicMetadata(ctx, tx, topic)
}

type registryRetirementRow struct {
	key  TopicKey
	root string
}

func (c *Catalog) retireRegistryRows(ctx context.Context, coordinator *historywork.Coordinator, seen map[TopicKey]bool, topics bool) error {
	var after TopicKey
	var pending []registryRetirementRow
	var exhausted bool
	return c.metadataSlices(ctx, coordinator, func(ctx context.Context, tx *sql.Tx, budget *metadataSlice) (bool, error) {
		if len(pending) == 0 {
			var err error
			pending, err = registryRetirementPage(ctx, tx, after, topics)
			if err != nil {
				return false, err
			}
			exhausted = len(pending) < historywork.BatchEntries
		}
		for len(pending) > 0 && budget.available() {
			row := pending[0]
			changed := false
			if !seen[row.key] {
				var err error
				changed, err = retireRegistryRow(ctx, tx, row.key, topics)
				if err != nil {
					return false, err
				}
			}
			budget.record(row.root, changed, len(row.root)+len(row.key.TopicID)+64)
			after, pending = row.key, pending[1:]
		}
		return exhausted && len(pending) == 0, nil
	})
}

func registryRetirementPage(ctx context.Context, tx *sql.Tx, after TopicKey, topics bool) ([]registryRetirementRow, error) {
	query := `SELECT scope,workspace_root_key,'',workspace_root FROM catalog_projects
 WHERE (scope,workspace_root_key)>(?,?) ORDER BY scope,workspace_root_key LIMIT ?`
	args := []any{after.Scope, after.workspaceKey, historywork.BatchEntries}
	if topics {
		query = `SELECT scope,workspace_root_key,topic_id,workspace_root FROM catalog_topics
 INDEXED BY idx_catalog_topics_registered_metadata WHERE metadata_present=1
 AND (scope,workspace_root_key,topic_id)>(?,?,?)
 ORDER BY scope,workspace_root_key,topic_id LIMIT ?`
		args = []any{after.Scope, after.workspaceKey, after.TopicID, historywork.BatchEntries}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []registryRetirementRow{}
	for rows.Next() {
		var row registryRetirementRow
		if err := rows.Scan(&row.key.Scope, &row.key.workspaceKey, &row.key.TopicID, &row.root); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func retireRegistryRow(ctx context.Context, tx *sql.Tx, key TopicKey, topics bool) (bool, error) {
	if !topics {
		return registryMutationChanged(tx.ExecContext(ctx, `DELETE FROM catalog_projects WHERE scope=? AND workspace_root_key=?`, key.Scope, key.workspaceKey))
	}
	changed, err := registryMutationChanged(tx.ExecContext(ctx, `UPDATE catalog_topics SET metadata_present=0
 WHERE scope=? AND workspace_root_key=? AND topic_id=? AND metadata_present=1`, key.Scope, key.workspaceKey, key.TopicID))
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM catalog_topics WHERE scope=? AND workspace_root_key=? AND topic_id=? AND `+orphanMetadataPredicate,
		key.Scope, key.workspaceKey, key.TopicID)
	return changed, err
}

func registryMutationChanged(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}
