package sessioncatalog

import "context"

// Caller holds mutationMu across preparation and publication so an obsolete
// source observation cannot overwrite a later committed mutation.
func (c *Catalog) prepareUpsertRecords(ctx context.Context, records []SessionRecord, dirtyDirectories map[string]DirectoryTarget, mode sessionUpsertMode) ([]SessionRecord, error) {
	filtered := records[:0]
	for _, record := range records {
		pathKey := c.pathKey(record.Path)
		if c.pathMutationAllowed(pathKey, record.enqueueSequence) {
			filtered = append(filtered, record)
		}
	}
	records = filtered
	if len(records) == 0 {
		return records, nil
	}
	if mode == upsertExactSource {
		prepared := make([]SessionRecord, 0, len(records))
		for _, raw := range records {
			record, skip, projectionDirty, err := c.prepareExactPathProjection(ctx, raw)
			if err != nil {
				return nil, err
			}
			if projectionDirty {
				dirtyDirectories[c.pathKey(record.Directory)] = DirectoryTarget{
					Path: record.Directory, Scope: record.Scope, WorkspaceRoot: record.WorkspaceRoot,
				}
			}
			if !skip {
				prepared = append(prepared, record)
			}
		}
		records = prepared
		if len(records) == 0 {
			return records, nil
		}
	}
	return records, nil
}
