package historycatalog

import (
	"context"
	"path/filepath"
	"strings"

	"reasonix/internal/retrieval"
)

func (c *Catalog) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	out := SearchResult{Items: []Candidate{}, Revision: c.revision.Load(), Partial: c.Status().Pending > 0}
	err := c.searchCandidates(ctx, req, false, func(item Candidate) error {
		out.Items = append(out.Items, item)
		return nil
	})
	return out, err
}

func (c *Catalog) searchCandidates(ctx context.Context, req SearchRequest, captureAll bool, visit func(Candidate) error) error {
	query, args, err := historySearchQuery(req, captureAll)
	if err != nil {
		return err
	}
	rows, err := c.searchDB(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Candidate
		if err := rows.Scan(&item.RowID, &item.SessionPath, &item.Root, &item.Source, &item.Scope, &item.WorkspaceRoot, &item.ContentDigest,
			&item.MessageIndex, &item.PartIndex, &item.Role, &item.Kind, &item.ToolName, &item.Rank,
			&item.SessionTitle, &item.TopicTitle, &item.LastActivityAt); err != nil {
			return err
		}
		if !catalogPathWithin(item.SessionPath, item.Root) {
			continue
		}
		if item.Rank < 0 {
			item.Score = -item.Rank
		} else {
			item.Score = 1 / (1 + item.Rank)
		}
		if err := visit(item); err != nil {
			return err
		}
	}
	return rows.Err()
}

func historySearchQuery(req SearchRequest, captureAll bool) (string, []any, error) {
	terms, err := retrieval.QueryTerms(req.Query)
	if err != nil {
		return "", nil, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if captureAll {
		limit = 2147483647
	}
	match := make([]string, 0, len(terms))
	for _, term := range terms {
		match = append(match, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	where := []string{`history_fts MATCH ?`, `s.health='ok'`, `s.missing_since=0`}
	args := []any{strings.Join(match, " OR ")}
	appendHistorySearchFilters(req, &where, &args)
	base := `SELECT d.id AS id,d.source_path AS source_path,s.root AS root,s.source AS source,s.scope AS scope,
		s.workspace_root AS workspace_root,s.content_digest AS content_digest,d.message_index AS message_index,
		d.part_index AS part_index,d.role AS role,d.kind AS kind,d.tool_name AS tool_name,bm25(history_fts) AS rank,
		s.custom_title AS custom_title,s.topic_title AS topic_title,s.last_activity_at AS last_activity_at
        FROM history_fts JOIN history_documents d ON d.id=history_fts.rowid JOIN history_sources s ON s.path=d.source_path
		WHERE ` + strings.Join(where, ` AND `)
	query := base + ` ORDER BY bm25(history_fts),d.source_path,d.message_index,d.part_index,d.id LIMIT ?`
	if after := req.After; after != nil {
		query = `WITH ranked AS MATERIALIZED (` + base + `)
			SELECT * FROM ranked WHERE rank>? OR (rank=? AND source_path>?) OR
			(rank=? AND source_path=? AND message_index>?) OR
			(rank=? AND source_path=? AND message_index=? AND part_index>?) OR
			(rank=? AND source_path=? AND message_index=? AND part_index=? AND id>?)
			ORDER BY rank,source_path,message_index,part_index,id LIMIT ?`
		args = append(args, after.Rank, after.Rank, after.SessionPath,
			after.Rank, after.SessionPath, after.MessageIndex,
			after.Rank, after.SessionPath, after.MessageIndex, after.PartIndex,
			after.Rank, after.SessionPath, after.MessageIndex, after.PartIndex, after.RowID)
	}
	return query, append(args, limit), nil
}

func appendHistorySearchFilters(req SearchRequest, where *[]string, args *[]any) {
	if req.Scope == "project" {
		*where = append(*where, `s.scope='project'`, `s.workspace_root=?`)
		*args = append(*args, strings.TrimSpace(req.WorkspaceRoot))
	}
	if path := strings.TrimSpace(req.SessionPath); path != "" {
		*where = append(*where, `d.source_path=?`)
		*args = append(*args, filepath.Clean(path))
	}
	if len(req.Kinds) > 0 {
		placeholders := make([]string, len(req.Kinds))
		for i, kind := range req.Kinds {
			placeholders[i] = "?"
			*args = append(*args, kind)
		}
		*where = append(*where, `d.kind IN (`+strings.Join(placeholders, ",")+`)`)
	}
	if tool := strings.TrimSpace(req.ToolName); tool != "" {
		*where = append(*where, `d.tool_name=?`)
		*args = append(*args, tool)
	}
	if len(req.Roots) > 0 {
		placeholders := make([]string, 0, len(req.Roots))
		for _, root := range req.Roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			*args = append(*args, filepath.Clean(root))
		}
		if len(placeholders) > 0 {
			*where = append(*where, `s.root IN (`+strings.Join(placeholders, ",")+`)`)
		}
	}
}

func catalogPathWithin(path, root string) bool {
	absPath, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
