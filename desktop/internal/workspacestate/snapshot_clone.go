package workspacestate

import (
	"bytes"
	"maps"
	"slices"
)

// Keep the decoded cache private. Every reader owns all of its mutable data,
// including fields retained for a newer writer that this version cannot see.
func cloneSnapshot(s State) State {
	s.TopicRemovals = maps.Clone(s.TopicRemovals)
	for id, removal := range s.TopicRemovals {
		removal.Snapshot = bytes.Clone(removal.Snapshot)
		removal.Metadata = bytes.Clone(removal.Metadata)
		removal.extra = cloneUnknownFields(removal.extra)
		s.TopicRemovals[id] = removal
	}
	s.extra = cloneUnknownFields(s.extra)
	s.WorkspaceIDs = slices.Clone(s.WorkspaceIDs)
	s.ArchivedSessionIDs = slices.Clone(s.ArchivedSessionIDs)
	s.Workspaces = maps.Clone(s.Workspaces)
	for id, w := range s.Workspaces {
		w.SessionIDs = slices.Clone(w.SessionIDs)
		w.FormerRoots = slices.Clone(w.FormerRoots)
		w.extra = cloneUnknownFields(w.extra)
		if w.Organization != nil {
			o := w.Organization.Clone()
			w.Organization = &o
		}
		s.Workspaces[id] = w
	}
	s.PendingCreates = maps.Clone(s.PendingCreates)
	for id, p := range s.PendingCreates {
		p.extra = cloneUnknownFields(p.extra)
		p.Presentation = clonePresentation(p.Presentation)
		s.PendingCreates[id] = p
	}
	s.SessionStates = maps.Clone(s.SessionStates)
	for id, status := range s.SessionStates {
		status.extra = cloneUnknownFields(status.extra)
		s.SessionStates[id] = status
	}
	s.SourceMappings = maps.Clone(s.SourceMappings)
	for id, m := range s.SourceMappings {
		s.SourceMappings[id] = cloneSourceMapping(m)
	}
	s.PendingOperations = maps.Clone(s.PendingOperations)
	for id, op := range s.PendingOperations {
		op.Request, op.Result = bytes.Clone(op.Request), bytes.Clone(op.Result)
		op.SessionIDs, op.Dependencies = slices.Clone(op.SessionIDs), slices.Clone(op.Dependencies)
		op.extra = cloneUnknownFields(op.extra)
		op.Presentation = clonePresentation(op.Presentation)
		if op.Mapping != nil {
			m := cloneSourceMapping(*op.Mapping)
			op.Mapping = &m
		}
		s.PendingOperations[id] = op
	}
	s.RecoveryEntries = maps.Clone(s.RecoveryEntries)
	for id, entry := range s.RecoveryEntries {
		entry.extra = cloneUnknownFields(entry.extra)
		s.RecoveryEntries[id] = entry
	}
	s.Presentation = maps.Clone(s.Presentation)
	for id, p := range s.Presentation {
		p.extra = cloneUnknownFields(p.extra)
		s.Presentation[id] = p
	}
	return s
}

func cloneSourceMapping(m SourceMapping) SourceMapping {
	m.RetainedArtifacts = slices.Clone(m.RetainedArtifacts)
	m.extra = cloneUnknownFields(m.extra)
	return m
}

func clonePresentation(p *Presentation) *Presentation {
	if p == nil {
		return nil
	}
	copy := *p
	copy.extra = cloneUnknownFields(p.extra)
	return &copy
}
