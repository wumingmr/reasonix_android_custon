package serve

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/jobs"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

var deleteSessionBeforeOwnershipLockHookForTest func()

// deleteSession removes a saved session by the session name returned from /sessions.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	sessionID := strings.TrimSpace(req.SessionID)
	if name == "" {
		name = sessionID
	}
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	// Validate the untrusted name before constructing any transcript or sidecar
	// path. IsLocal also rejects Windows drive-relative and reserved names;
	// the separator check keeps this endpoint restricted to one basename.
	if !filepath.IsLocal(name) || name == "." || strings.ContainsAny(name, `/\`) {
		http.Error(w, "invalid session name", http.StatusBadRequest)
		return
	}
	// Serialize the ownership checks with session promotion: a detached
	// controller leaves the background registry while it is promoted, and a
	// delete crossing that window would remove a live controller's transcript.
	if deleteSessionBeforeOwnershipLockHookForTest != nil {
		deleteSessionBeforeOwnershipLockHookForTest()
	}
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	dir := s.ctl().SessionDir()
	if sessionID != "" && s.canonicalSessionIsCurrent(sessionID) {
		// Never let a colliding legacy basename take precedence over the active
		// canonical identity.
		s.deleteCanonicalSession(w, r, sessionID)
		return
	}
	// A canonical session lives in <sessions-v4>/<sessionId>/, not
	// <legacy-dir>/<name>.jsonl. Prefer an existing legacy file so the old
	// endpoint stays compatible, then fall back to the immutable identity.
	legacyExists := false
	if dir != "" {
		_, statErr := os.Stat(filepath.Join(dir, name+".jsonl"))
		legacyExists = statErr == nil
	}
	if sessionID != "" && !legacyExists {
		if s.deleteCanonicalSession(w, r, sessionID) {
			return
		}
	}
	if dir == "" {
		http.Error(w, "sessions disabled", http.StatusBadRequest)
		return
	}
	target := filepath.Join(dir, name+".jsonl")
	abs, err := filepath.Abs(target)
	if err != nil {
		http.Error(w, "invalid session path", http.StatusBadRequest)
		return
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, "invalid session dir", http.StatusBadRequest)
		return
	}
	rel, err := filepath.Rel(absDir, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		http.Error(w, "path outside session dir", http.StatusForbidden)
		return
	}
	if msg, status := s.legacyTranscriptDeleteRefusalLocked(abs); msg != "" {
		http.Error(w, msg, status)
		return
	}
	if err := s.destroyLegacyTranscriptLocked(absDir, abs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// legacyTranscriptDeleteRefusalLocked is the ownership gate every legacy
// transcript removal passes, whether the user named the transcript or it is
// the frozen source of a canonical row being deleted. Callers hold bindMu so a
// concurrent promotion cannot move the transcript between checks.
func (s *Server) legacyTranscriptDeleteRefusalLocked(abs string) (string, int) {
	if filepath.Clean(abs) == filepath.Clean(s.ctl().SessionPath()) {
		return "cannot delete active session", http.StatusConflict
	}
	if s.detachedBusy(filepath.Clean(abs)) {
		return "session is running in the background; switch to it and stop the turn first", http.StatusConflict
	}
	if s.sessionMirrored(abs) {
		// A local runtime is writing this transcript; deleting it here would
		// pull the file out from under the writer.
		return "session is taken over by a local Reasonix window", http.StatusConflict
	}
	return "", 0
}

// destroyLegacyTranscriptLocked tears down session-scoped jobs and removes the
// transcript with its sidecars. A teardown that outlives its grace period marks
// the transcript for delayed cleanup instead of leaving live jobs writing beside
// a half-removed session; the marker error is the only failure that still
// schedules the delayed removal.
func (s *Server) destroyLegacyTranscriptLocked(absDir, abs string) error {
	destroy := s.ctl().BeginDestroySession(abs)
	if result := finishSessionDestroy(destroy); result.HasTimedOut() {
		err := agent.MarkCleanupPending(abs, "delete")
		go delayedSessionDelete(absDir, abs, destroy)
		return err
	}
	return removeSessionFiles(absDir, abs)
}

// deleteCanonicalSession deletes one canonical identity and reports whether
// the request was handled. It is called with bindMu held so a concurrent
// /resume or /new cannot change the foreground identity between the active
// check and the filesystem tombstone.
func (s *Server) deleteCanonicalSession(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	identity, ok := s.ctl().(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		return false
	}
	service := identity.SessionService()
	if service == nil {
		http.Error(w, "canonical sessions disabled", http.StatusBadRequest)
		return true
	}
	ref := session.SessionRef{HostID: service.HostID(), SessionID: sessionID}
	if current, bound := identity.SessionRef(); bound && current == ref {
		http.Error(w, "cannot delete active session", http.StatusConflict)
		return true
	}
	// Resolve the frozen source before the row goes: the shared index counts
	// live targets only. A refusal from the source's ownership gate stops the
	// whole request rather than deleting the row and resurrecting the source.
	source, hasSource, msg, status := s.migratedSourceForDeleteLocked(r.Context(), service, ref)
	if msg != "" {
		http.Error(w, msg, status)
		return true
	}
	if err := service.Delete(r.Context(), ref); err != nil {
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, session.ErrRuntimeBusy), errors.Is(err, session.ErrRuntimeBound):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return true
	}
	if hasSource {
		// Without its row the source resurfaces in /sessions as a fresh legacy
		// row. A teardown failure leaves only that resurfacing, so it is
		// reported instead of failing the delete the user asked for.
		if err := s.destroyLegacyTranscriptLocked(filepath.Dir(source), source); err != nil {
			slog.Warn("serve: remove migrated legacy source after canonical delete", "source", source, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

// migratedSourceForDeleteLocked returns the legacy transcript that ref is the
// sole canonical target of, already validated as a deletable transcript inside
// the session dir. A source outside the session dir (or otherwise unresolvable)
// is left alone without blocking the canonical delete; a source that is active,
// running detached or mirrored returns the legacy delete's refusal. Callers
// hold bindMu.
func (s *Server) migratedSourceForDeleteLocked(ctx context.Context, service *session.Service, ref session.SessionRef) (source string, ok bool, refusal string, status int) {
	dir, err := service.SessionDir(ctx, ref)
	if err != nil {
		return "", false, "", 0
	}
	index := loadMigrationIndex(map[string]struct{}{filepath.Dir(filepath.Clean(dir)): {}}, func(targetID string) bool {
		_, statErr := service.SessionDir(ctx, session.SessionRef{HostID: ref.HostID, SessionID: targetID})
		return statErr == nil
	})
	recorded, ok := index.byTarget[ref.SessionID]
	if !ok {
		return "", false, "", 0
	}
	realPath, err := s.resolveSessionPath(recorded)
	if err != nil {
		slog.Warn("serve: migrated legacy source is not a deletable transcript; leaving it", "source", recorded, "err", err)
		return "", false, "", 0
	}
	abs := filepath.Clean(realPath)
	if msg, code := s.legacyTranscriptDeleteRefusalLocked(abs); msg != "" {
		return "", false, msg, code
	}
	return abs, true, "", 0
}

func (s *Server) canonicalSessionIsCurrent(sessionID string) bool {
	identity, ok := s.ctl().(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		return false
	}
	service := identity.SessionService()
	if service == nil {
		return false
	}
	current, bound := identity.SessionRef()
	return bound && current == (session.SessionRef{HostID: service.HostID(), SessionID: sessionID})
}

func finishSessionDestroy(destroy control.SessionDestroyHandle) jobs.TeardownResult {
	if destroy.Wait != nil {
		result := destroy.Wait()
		if destroy.Finish != nil && !result.HasTimedOut() {
			destroy.Finish()
		}
		return result
	}
	if destroy.Finish != nil {
		destroy.Finish()
	}
	return jobs.TeardownResult{}
}

func delayedSessionDelete(absDir, abs string, destroy control.SessionDestroyHandle) {
	if destroy.WaitAll != nil {
		destroy.WaitAll()
	}
	if err := removeSessionFiles(absDir, abs); err != nil {
		slog.Warn("serve: delayed session delete failed", "path", abs, "err", err)
	}
	if destroy.Finish != nil {
		destroy.Finish()
	}
}

func removeSessionFiles(absDir, abs string) error {
	remove := append([]string{abs}, store.SessionSidecarFiles(abs)...)
	for _, p := range remove {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := agent.DeleteSubagentsByParent(absDir, agent.BranchID(abs)); err != nil {
		return err
	}
	if err := jobs.RemoveArtifacts(abs); err != nil {
		return err
	}
	return agent.ClearCleanupPending(abs)
}
