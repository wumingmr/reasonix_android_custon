package main

import (
	"strings"

	"reasonix/internal/sessioncatalog"
)

func projectTopicLess(left, right ProjectNode, sortMode string, manualOrder bool) bool {
	if left.Pinned != right.Pinned {
		return left.Pinned
	}
	if manualOrder {
		leftRank, rightRank := left.SortOrder, right.SortOrder
		if leftRank < 0 {
			leftRank = int(^uint(0) >> 1)
		}
		if rightRank < 0 {
			rightRank = int(^uint(0) >> 1)
		}
		if leftRank != rightRank {
			return leftRank < rightRank
		}
	}
	leftActivity := projectTopicSortValue(left.CreatedAt, left.LastActivityAt, sortMode)
	rightActivity := projectTopicSortValue(right.CreatedAt, right.LastActivityAt, sortMode)
	if leftActivity != rightActivity {
		return leftActivity > rightActivity
	}
	if left.TopicID != right.TopicID {
		return left.TopicID < right.TopicID
	}
	return left.Key < right.Key
}

func manualTopicOrderFor(scope, workspaceRoot string) bool {
	f := loadProjectsFile()
	if strings.TrimSpace(scope) != "project" {
		return f.GlobalManualSessionOrder || f.GlobalManualTopicOrder
	}
	if index := projectIndexByRoot(f.Projects, workspaceRoot); index >= 0 {
		return f.Projects[index].ManualSessionOrder || f.Projects[index].ManualTopicOrder
	}
	return false
}

func manualSessionOrderFor(scope, workspaceRoot string) bool {
	f := loadProjectsFile()
	if strings.TrimSpace(scope) != "project" {
		return f.GlobalManualSessionOrder
	}
	if index := projectIndexByRoot(f.Projects, workspaceRoot); index >= 0 {
		return f.Projects[index].ManualSessionOrder
	}
	return false
}

func sessionOrderRank(projects desktopProjectFile, scope, workspaceRoot, key string) int {
	order := projects.GlobalSessionOrder
	manual := projects.GlobalManualSessionOrder
	if strings.TrimSpace(scope) == "project" {
		if index := projectIndexByRoot(projects.Projects, workspaceRoot); index >= 0 {
			order = projects.Projects[index].SessionOrder
			manual = projects.Projects[index].ManualSessionOrder
		}
	}
	if !manual {
		return -1
	}
	for index, candidate := range order {
		if candidate == key {
			return index
		}
	}
	return -1
}

func encodeProjectTopicCursor(topic sessioncatalog.TopicRecord, sortMode string, manualOrder bool, binding string) string {
	pinned := 0
	if topic.Pinned {
		pinned = 1
	}
	activity := projectTopicSortValue(topic.CreatedAt, topic.LastActivityAt, sortMode)
	if manualOrder {
		return sessioncatalog.EncodeOrderedTopicCursorBound(pinned, topic.SortOrder, activity, topic.TopicID, binding)
	}
	return sessioncatalog.EncodeTopicCursorBound(pinned, activity, topic.TopicID, binding)
}

func encodeProjectNodeCursor(topic ProjectNode, sortMode string, manualOrder bool, binding string) string {
	pinned := 0
	if topic.Pinned {
		pinned = 1
	}
	activity := projectTopicSortValue(topic.CreatedAt, topic.LastActivityAt, sortMode)
	if manualOrder {
		return sessioncatalog.EncodeOrderedTopicCursorBound(pinned, topic.SortOrder, activity, topic.TopicID, binding)
	}
	return sessioncatalog.EncodeTopicCursorBound(pinned, activity, topic.TopicID, binding)
}
