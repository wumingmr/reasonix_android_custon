package sessioncatalog

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"reasonix/internal/historywork"
)

// Registry input is already a complete caller-owned observation. Applying it
// is incremental: additions finish before retirement starts, and each slice
// releases the common writer. Cancellation never retires unvisited input.
// No second authoritative registry or persisted format is introduced.
func (c *Catalog) syncMetadataIncremental(ctx context.Context, projects []ProjectRecord, topics []TopicMetadata) error {
	coordinator := c.opts.Maintenance
	if coordinator == nil {
		coordinator = &historywork.Coordinator{}
	}
	seenProjects := map[TopicKey]bool{}
	seenTopics := map[TopicKey]bool{}
	projectIndex, topicIndex := 0, 0
	err := c.metadataSlices(ctx, coordinator, func(ctx context.Context, tx *sql.Tx, budget *metadataSlice) (bool, error) {
		if _, err := tx.ExecContext(ctx, metadataTopicIndex); err != nil {
			return false, err
		}
		for projectIndex < len(projects) && budget.available() {
			project := projects[projectIndex]
			project.Scope, project.WorkspaceRoot = normalizeScope(project.Scope, project.WorkspaceRoot)
			key := TopicKey{Scope: project.Scope, workspaceKey: c.workspaceRootKey(project.Scope, project.WorkspaceRoot)}
			changed, err := c.projectRegistryRow(ctx, tx, project, key.workspaceKey)
			if err != nil {
				return false, err
			}
			seenProjects[key] = true
			budget.record(project.WorkspaceRoot, changed, len(project.Title)+len(project.WorkspaceRoot)+len(project.Color)+64)
			projectIndex++
		}
		for topicIndex < len(topics) && budget.available() {
			topic := topics[topicIndex]
			topic.Scope, topic.WorkspaceRoot = normalizeScope(topic.Scope, topic.WorkspaceRoot)
			if strings.TrimSpace(topic.TopicID) != "" {
				key := TopicKey{Scope: topic.Scope, workspaceKey: c.workspaceRootKey(topic.Scope, topic.WorkspaceRoot), TopicID: topic.TopicID}
				skip, err := c.skipFoldedRecoveryShell(ctx, tx, topic)
				if err != nil {
					return false, err
				}
				changed := false
				if !skip {
					changed, err = c.topicRegistryRow(ctx, tx, topic, key.workspaceKey)
					if err != nil {
						return false, err
					}
					seenTopics[key] = true
				}
				budget.record(topic.WorkspaceRoot, changed, len(topic.Title)+len(topic.TopicID)+len(topic.WorkspaceRoot)+64)
			} else {
				budget.record("", false, 1)
			}
			topicIndex++
		}
		return projectIndex == len(projects) && topicIndex == len(topics), nil
	})
	if err != nil {
		return err
	}
	// Keyset pages cover only registered rows, including rows written by older
	// versions. Session-derived topics are owned by source reconciliation.
	if err := c.retireRegistryRows(ctx, coordinator, seenProjects, false); err != nil {
		return err
	}
	return c.retireRegistryRows(ctx, coordinator, seenTopics, true)
}

type metadataSlice struct {
	started time.Time
	entries int
	bytes   int64
	roots   map[string]struct{}
}

func (s *metadataSlice) available() bool {
	// Always admit one item, even when scheduler latency consumed the slice.
	return s.entries == 0 || s.entries < historywork.BatchEntries && s.bytes < historywork.BatchBytes && time.Since(s.started) < historywork.SliceDuration
}

func (s *metadataSlice) record(root string, changed bool, bytes int) {
	s.entries++
	s.bytes += int64(bytes)
	if changed {
		s.roots[root] = struct{}{}
	}
}

func (c *Catalog) metadataSlices(ctx context.Context, coordinator *historywork.Coordinator, step func(context.Context, *sql.Tx, *metadataSlice) (bool, error)) error {
	for {
		release, err := coordinator.BackgroundSlice(ctx, false)
		if err != nil {
			return err
		}
		budget := &metadataSlice{started: time.Now(), roots: map[string]struct{}{}}
		done, err := c.commitMetadataSlice(ctx, budget, step)
		release(budget.bytes)
		if err != nil {
			return err
		}
		if c.testMetadataSliceHook != nil {
			c.testMetadataSliceHook(budget.entries)
		}
		if done {
			return nil
		}
	}
}

func (c *Catalog) commitMetadataSlice(ctx context.Context, budget *metadataSlice, step func(context.Context, *sql.Tx, *metadataSlice) (bool, error)) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	done, err := step(ctx, tx, budget)
	if err != nil {
		return false, err
	}
	var revision uint64
	if len(budget.roots) > 0 {
		revision, err = bumpRevision(ctx, tx)
		if err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if revision != 0 {
		c.publishRevision(revision, mapKeys(budget.roots), "metadata")
	}
	return done, nil
}
