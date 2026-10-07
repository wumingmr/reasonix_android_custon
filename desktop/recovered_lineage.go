package main

import (
	"os"
	"path/filepath"
	"slices"

	"reasonix/internal/agent"
	"reasonix/internal/store"
)

// recoveredLegacySiblings lists the other recovery snapshots of the lineage a
// recovered legacy source belongs to. Writers record either the shared root or
// the previous snapshot as ParentID, so the lineage is the set of recovered
// transcripts in the same directory whose ParentID chain ends at the same id.
func recoveredLegacySiblings(source historicalSource) ([]string, error) {
	if source.format != "legacy" {
		return nil, nil
	}
	dir := filepath.Dir(source.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	parents := map[string]string{}
	paths := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !store.IsSessionTranscriptName(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		meta, ok, err := agent.LoadBranchMeta(path)
		if err != nil || !ok || !meta.Recovered {
			continue
		}
		id := agent.BranchID(path)
		parents[id] = meta.ParentID
		paths[id] = path
	}
	self := agent.BranchID(source.path)
	if _, recovered := parents[self]; !recovered {
		return nil, nil
	}
	root := lineageRoot(self, parents)
	var siblings []string
	for id, path := range paths {
		if id != self && lineageRoot(id, parents) == root {
			siblings = append(siblings, path)
		}
	}
	slices.Sort(siblings)
	return siblings, nil
}

func lineageRoot(id string, parents map[string]string) string {
	for range len(parents) + 1 {
		parent, ok := parents[id]
		if !ok || parent == "" || parent == id {
			return id
		}
		if _, recovered := parents[parent]; !recovered {
			return parent
		}
		id = parent
	}
	return id
}
