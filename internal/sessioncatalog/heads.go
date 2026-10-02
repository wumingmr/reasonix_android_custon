package sessioncatalog

import (
	"context"
	"database/sql"
	"fmt"

	"reasonix/internal/agent"
)

// HeadRecord is one head of a schema-2 session as the catalog projects it.
// Rows come from the kernel's head index sidecar, never from replaying a log.
type HeadRecord struct {
	Path           string `json:"path"`
	ID             string `json:"id"`
	ParentHeadID   string `json:"parentHeadId,omitempty"`
	Kind           string `json:"kind"`
	Name           string `json:"name,omitempty"`
	LeafMessageID  string `json:"leafMessageId,omitempty"`
	WriterID       string `json:"writerId,omitempty"`
	LastActivityAt int64  `json:"lastActivityAt,omitempty"`
	Turns          int    `json:"turns"`
	Preview        string `json:"preview,omitempty"`
	Retired        bool   `json:"retired,omitempty"`
	Selected       bool   `json:"selected,omitempty"`
}

// sessionHeadProjection is what recordFromOrder learns about a schema-2
// session without opening its log: the sidecar mirror is always available,
// the per-head rows only while the head index still matches the log.
type sessionHeadProjection struct {
	logFormat   int
	headCount   int
	selected    string
	fingerprint string
	heads       []HeadRecord
	stale       bool
}

func projectSessionHeads(info agent.SessionOrderInfo) sessionHeadProjection {
	if info.LogSchema < 2 {
		return sessionHeadProjection{logFormat: 1}
	}
	out := sessionHeadProjection{logFormat: info.LogSchema, headCount: info.HeadCount, selected: info.HeadID}
	idx, err := agent.ReadSessionHeadIndex(info.Path)
	if err != nil || idx == nil || !idx.Current(info.Path) {
		out.stale = true
		return out
	}
	out.selected = idx.SelectedHead
	out.headCount = len(idx.Heads)
	out.heads, out.fingerprint = headRecordsFromIndex(info.Path, idx)
	return out
}

func headRecordsFromIndex(path string, idx *agent.SessionHeadIndex) ([]HeadRecord, string) {
	heads := make([]HeadRecord, 0, len(idx.Heads))
	fingerprint := ""
	for _, h := range idx.Heads {
		heads = append(heads, HeadRecord{
			Path: path, ID: h.ID, ParentHeadID: h.ParentHead, Kind: h.Kind, Name: h.Name,
			LeafMessageID: h.LeafID, WriterID: h.Writer, LastActivityAt: unixMilli(h.LastActivity),
			Turns: h.Turns, Preview: h.Preview, Retired: h.Retired, Selected: h.ID == idx.SelectedHead,
		})
		if h.ID == idx.SelectedHead {
			fingerprint = "|h:" + h.ID + ":" + h.LeafID
		}
	}
	return heads, fingerprint
}

// projectRepairedHeads lands the head rows of a session whose repair just
// completed inside that transaction: repair rebuilds a stale head index, and
// the row must not turn valid while its head_count names rows it lacks.
func projectRepairedHeads(ctx context.Context, tx *sql.Tx, path, pathKey string) error {
	idx, err := agent.ReadSessionHeadIndex(path)
	if err != nil || idx == nil || !idx.Current(path) {
		return nil
	}
	heads, _ := headRecordsFromIndex(path, idx)
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_sessions SET log_format=?,head_count=?,selected_head_id=?
		WHERE path_key=?`, idx.SchemaVersion, len(heads), idx.SelectedHead, pathKey); err != nil {
		return err
	}
	return upsertHeadRows(ctx, tx, pathKey, heads)
}

// writeDirectoryRow lands one scanned session row and its head rows inside
// the directory projection transaction.
func (c *Catalog) writeDirectoryRow(ctx context.Context, tx *sql.Tx, stmt *sql.Stmt, record SessionRecord, pathKey, directoryKey string, generation int64) error {
	if _, err := stmt.ExecContext(ctx, c.sessionRowValues(record, pathKey, directoryKey, generation)...); err != nil {
		return err
	}
	return upsertHeadRows(ctx, tx, pathKey, record.heads)
}

// upsertHeadRows replaces the head rows of one session inside the projection
// transaction. Stale projections keep the previous rows until the head index
// is current again.
func upsertHeadRows(ctx context.Context, tx *sql.Tx, pathKey string, heads []HeadRecord) error {
	if heads == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_heads WHERE path_key=?`, pathKey); err != nil {
		return err
	}
	for _, h := range heads {
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_heads(path_key,head_id,parent_head_id,kind,name,leaf_message_id,
			writer_id,last_activity_at,turns,preview,retired,selected) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			pathKey, h.ID, h.ParentHeadID, h.Kind, h.Name, h.LeafMessageID, h.WriterID, h.LastActivityAt, h.Turns, h.Preview,
			boolToInt(h.Retired), boolToInt(h.Selected)); err != nil {
			return err
		}
	}
	return nil
}

// ListHeads returns the projected heads of one session in creation order. A
// schema-1 session has none and returns an empty slice.
func (c *Catalog) ListHeads(ctx context.Context, path string) ([]HeadRecord, error) {
	out := []HeadRecord{}
	if c == nil || path == "" {
		return out, nil
	}
	rows, err := c.readDB(ctx).QueryContext(ctx, `SELECT head_id,parent_head_id,kind,name,leaf_message_id,writer_id,last_activity_at,
		turns,preview,retired,selected FROM catalog_heads WHERE path_key=? ORDER BY rowid`, c.pathKey(path))
	if err != nil {
		return out, fmt.Errorf("list session heads: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h HeadRecord
		var retired, selected int
		if err := rows.Scan(&h.ID, &h.ParentHeadID, &h.Kind, &h.Name, &h.LeafMessageID, &h.WriterID, &h.LastActivityAt,
			&h.Turns, &h.Preview, &retired, &selected); err != nil {
			return out, err
		}
		h.Path = path
		h.Retired, h.Selected = retired != 0, selected != 0
		out = append(out, h)
	}
	return out, rows.Err()
}
