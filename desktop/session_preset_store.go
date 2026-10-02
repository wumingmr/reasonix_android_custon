package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const sessionPresetsFileName = "desktop-session-presets.json"

type sessionPresetsFile struct {
	Version  int               `json:"version"`
	Sessions map[string]string `json:"sessions"`
}

// sessionPresetStore holds the permission preset the user explicitly chose for
// each canonical session, keyed by SessionID. Only record writes to it, and only
// the explicit preset-change entry points call record; a session is restored
// to its own record or, without one, to the new-session default.
type sessionPresetStore struct {
	mu       sync.Mutex
	loaded   bool
	unsaved  bool
	recorded map[string]string
}

func newSessionPresetStore() *sessionPresetStore {
	return &sessionPresetStore{}
}

func sessionPresetsPath() string {
	return filepath.Join(desktopConfigDir(), sessionPresetsFileName)
}

func (s *sessionPresetStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.recorded = map[string]string{}
	body, err := os.ReadFile(sessionPresetsPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var file sessionPresetsFile
	if err == nil {
		err = json.Unmarshal(body, &file)
	}
	if err != nil {
		// Unreadable records restore nothing: every session opens at the default.
		slog.Warn("desktop: session permission presets unreadable", "err", err)
		return
	}
	for id, preset := range file.Sessions {
		if id = strings.TrimSpace(id); id != "" {
			s.recorded[id] = normalizeToolApprovalMode(preset)
		}
	}
}

// restore returns the preset recorded for sessionID, or fallback when the
// session has none.
func (s *sessionPresetStore) restore(sessionID, fallback string) string {
	sessionID = strings.TrimSpace(sessionID)
	fallback = normalizeToolApprovalMode(fallback)
	if s == nil || sessionID == "" {
		return fallback
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if preset, ok := s.recorded[sessionID]; ok {
		return preset
	}
	return fallback
}

// record stores an explicit user choice for the session it was made in.
func (s *sessionPresetStore) record(sessionID, preset string) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || sessionID == "" {
		return
	}
	preset = normalizeToolApprovalMode(preset)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if recorded, ok := s.recorded[sessionID]; ok && recorded == preset && !s.unsaved {
		return
	}
	s.recorded[sessionID] = preset
	s.writeLocked()
}

func (s *sessionPresetStore) forget(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if _, ok := s.recorded[sessionID]; !ok && !s.unsaved {
		return
	}
	delete(s.recorded, sessionID)
	s.writeLocked()
}

func (s *sessionPresetStore) writeLocked() {
	body, err := json.MarshalIndent(sessionPresetsFile{Version: 1, Sessions: s.recorded}, "", "  ")
	if err == nil {
		err = os.MkdirAll(desktopConfigDir(), 0o700)
	}
	if err == nil {
		err = writeFileAtomic(sessionPresetsPath(), body, 0o600)
	}
	// The file must not go on holding a wider preset than this process runs
	// with: a failed write drops or empties it, so a restart opens at the
	// default, and the next record or forget writes the full set again.
	s.unsaved = err != nil
	if err != nil {
		slog.Warn("desktop: session permission presets not saved", "err", err)
		discardSessionPresetsFile()
	}
}

// discardSessionPresetsFile removes the file, or empties it in place when its
// directory refuses the removal; an empty file restores nothing.
func discardSessionPresetsFile() {
	path := sessionPresetsPath()
	rmErr := os.Remove(path)
	if rmErr == nil || errors.Is(rmErr, os.ErrNotExist) {
		return
	}
	if err := os.Truncate(path, 0); err != nil {
		slog.Warn("desktop: stale session permission presets not discarded", "remove", rmErr, "truncate", err)
	}
}
