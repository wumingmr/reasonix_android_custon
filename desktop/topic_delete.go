package main

// DeleteTopic removes a topic and its title metadata.
func (a *App) DeleteTopic(topicID string) error {
	return friendlySessionFileError(a.deleteTopic(topicID))
}

func (a *App) deleteTopic(topicID string) error {
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	topicIndexMu.Lock()
	defer topicIndexMu.Unlock()
	return a.deleteTopicMutationLocked(topicID)
}

func (a *App) deleteTopicMutationLocked(topicID string) error {
	desktopProjectsFileMu.Lock()
	defer desktopProjectsFileMu.Unlock()
	return a.deleteTopicMutationProjectsLocked(topicID)
}

func (a *App) deleteTopicMutationProjectsLocked(topicID string) error {
	release, err := acquireDesktopProjectsFileLock()
	if err != nil {
		return err
	}
	defer release()
	return a.deleteTopicMutationProjectsCrossProcessLocked(topicID)
}

// deleteTopicMutationProjectsCrossProcessLocked requires the process and file
// locks for desktop-projects.json.
func (a *App) deleteTopicMutationProjectsCrossProcessLocked(topicID string) error {
	f := loadProjectsFile()
	indexed := map[string]bool{
		"": containsDesktopString(f.GlobalTopics, topicID) || containsDesktopString(f.GlobalPinnedTopics, topicID),
	}
	roots := make([]string, 0, len(f.Projects)+1)
	for _, project := range f.Projects {
		roots = append(roots, project.Root)
		indexed[project.Root] = containsDesktopString(project.Topics, topicID) || containsDesktopString(project.PinnedTopics, topicID)
	}
	// The tombstone prevents stale migration and repair snapshots from
	// resurrecting the topic while its per-scope metadata is removed.
	if err := removeTopicFromProjectsFileCrossProcessLocked(topicID); err != nil {
		return err
	}
	roots = append(roots, "")
	for _, root := range roots {
		titles, err := loadTopicTitlesForUpdate(root)
		if err != nil {
			if indexed[root] {
				return err
			}
			continue
		}
		if _, hasTitle := titles[topicID]; !hasTitle && !indexed[root] {
			continue
		}
		if err := deleteTopicState(root, topicID); err != nil {
			return err
		}
	}
	a.emitProjectTreeMetadataChanged()
	return nil
}
