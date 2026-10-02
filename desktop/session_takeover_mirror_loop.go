package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/eventwire"
)

func (m *takeoverMirror) run(initialClient *http.Client, initialRecord takeoverServeRecord) {
	defer close(m.done)
	m.mu.Lock()
	if m.client == nil {
		m.client = initialClient
		m.record = initialRecord
	}
	m.mu.Unlock()

	flushTimer := time.NewTimer(time.Hour)
	if !flushTimer.Stop() {
		<-flushTimer.C
	}
	heartbeat := time.NewTicker(takeoverMirrorHeartbeat)
	retryReturn := time.NewTicker(250 * time.Millisecond)
	defer flushTimer.Stop()
	defer heartbeat.Stop()
	defer retryReturn.Stop()
	flushArmed := false
	for {
		select {
		case <-m.stop:
			m.flushOnce(context.Background())
			return
		case <-m.wake:
			if !flushArmed {
				flushTimer.Reset(takeoverMirrorFlushEvery)
				flushArmed = true
			}
			continue
		case <-flushTimer.C:
			flushArmed = false
			if !m.pushOnce(false) {
				return
			}
		case <-heartbeat.C:
			if !m.pushOnce(true) {
				return
			}
		case <-retryReturn.C:
			if m.retryPendingReturn(false) {
				m.detach()
				m.mirrorEnd()
				return
			}
		}
		// A mirror whose tab is gone entirely (closed, not detached) ends
		// itself so Serve can hand the session back. A tab close sends its own
		// farewell after releasing the writer, so this only detaches for it.
		if !m.app.takeoverTabLive(m.sessionPath) {
			m.detach()
			if !m.closing.Load() {
				m.mirrorEnd()
			}
			return
		}
	}
}

func (m *takeoverMirror) pushOnce(heartbeat bool) bool {
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	return m.pushOnceLocked(heartbeat)
}

func (m *takeoverMirror) pushOnceLocked(heartbeat bool) bool {
	if m.returned.Load() {
		return false
	}
	client, record, _, grant, revision := m.snapshotBinding()
	if client == nil || grant.MirrorID == "" {
		return true
	}
	frames := m.drainQueue()
	if len(frames) == 0 && !heartbeat {
		return true
	}
	marshal := func(batch []eventwire.Event) ([]byte, error) {
		return json.Marshal(map[string]any{
			"sessionPath": m.sessionPath, "mirrorId": grant.MirrorID, "frames": batch,
		})
	}
	batch, remainder, payload, err := eventwire.MarshalMirrorBatch(frames, eventwire.MirrorBatchMaxBytes, marshal)
	if err == nil && len(batch) == 0 && len(frames) > 0 && len(remainder) > 0 {
		remainder = remainder[1:]
	}
	m.requeue(remainder)
	if err != nil {
		m.requeue(batch)
		return true
	}
	if len(batch) == 0 && len(frames) > 0 {
		m.wakeIfQueued()
		if !heartbeat {
			return true
		}
		payload, _ = marshal(nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	resp, err := serveDo(ctx, client, http.MethodPost, serveURL(record.base, "/external/frames"), payload)
	if err != nil {
		cancel()
		if !m.bindingCurrent(client, grant, revision) {
			return true
		}
		m.requeue(batch)
		return m.retryAdoptOrDemote(client, grant, revision)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	cancel()
	if !m.bindingCurrent(client, grant, revision) {
		return true
	}
	switch resp.StatusCode {
	case http.StatusOK:
		m.mu.Lock()
		if m.bindingRevision == revision {
			m.consecutiveFailures = 0
		}
		m.mu.Unlock()
		var out struct {
			ReclaimRequested bool   `json:"reclaimRequested"`
			ReclaimMode      string `json:"reclaimMode"`
		}
		if json.Unmarshal(body, &out) == nil && out.ReclaimRequested {
			m.requestDemote(out.ReclaimMode)
		}
		m.wakeIfQueued()
		return true
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict:
		slog.Info("desktop: mirror generation rejected — attempting re-adopt",
			"session", m.sessionPath)
		m.requeue(batch)
		return m.retryAdoptOrDemote(client, grant, revision)
	default:
		m.requeue(batch)
		m.mu.Lock()
		if m.bindingRevision == revision {
			m.consecutiveFailures++
		}
		failures := m.consecutiveFailures
		m.mu.Unlock()
		if failures < 3 {
			return true
		}
		return m.retryAdoptOrDemote(client, grant, revision)
	}
}

// retryAdoptOrDemote attempts to re-establish the mirror with fresh serve
// credentials (the serve may have restarted with a new token). If the serve
// already owns the session (reclaim completed or another writer took over),
// demotes this tab to read-only and releases the lease so the remote side
// can proceed. Returns true if re-adopted (caller continues the loop).
func (m *takeoverMirror) retryAdoptOrDemote(oldClient *http.Client, oldGrant takeoverGrant, revision uint64) bool {
	if !m.bindingCurrent(oldClient, oldGrant, revision) {
		return true
	}
	conflicted := false
	records := discoverLocalTakeoverServesForMirror()
	for _, record := range records {
		if !pathWithinDir(m.sessionPath, config.ProjectSessionDir(record.state.Workspace)) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		client, err := takeoverClient(ctx, record)
		if err != nil {
			cancel()
			continue
		}
		body, bodyErr := json.Marshal(map[string]string{"sessionPath": m.sessionPath, "writerId": agent.SessionWriterID()})
		if bodyErr != nil {
			cancel()
			continue
		}
		resp, respErr := serveDo(ctx, client, http.MethodPost, serveURL(record.base, "/adopt"), body)
		cancel()
		if respErr != nil {
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var grant takeoverGrant
			if json.Unmarshal(respBody, &grant) != nil || grant.MirrorID == "" || grant.ReturnHandoffID == "" || grant.SourceWriterID == "" ||
				grant.TargetWriterID != agent.SessionWriterID() || sessionRuntimeKey(grant.SessionPath) != sessionRuntimeKey(m.sessionPath) {
				continue
			}
			// Re-adopted: swap in the fresh client and keep mirroring.
			m.mu.Lock()
			if m.client == oldClient && m.grant.MirrorID == oldGrant.MirrorID && m.bindingRevision == revision {
				m.client = client
				m.record = record
				m.grant = grant
				m.bindingRevision++
				m.consecutiveFailures = 0
			}
			m.mu.Unlock()
			slog.Info("desktop: mirror re-adopted with fresh credentials",
				"session", m.sessionPath, "base", record.base)
			return true
		}
		if resp.StatusCode == http.StatusConflict {
			conflicted = true
			continue
		}
		// Other statuses: try next record.
	}
	if conflicted && m.bindingCurrent(oldClient, oldGrant, revision) {
		slog.Info("desktop: serve holds session — demoting to release lease",
			"session", m.sessionPath)
		m.requestDemote("")
		return false
	}
	// The Serve may be restarting or its state/token files may not have become
	// visible yet. Keep the bounded queue and retry on the next heartbeat.
	slog.Warn("desktop: mirror re-adopt unavailable; retaining local writer and bounded queue",
		"session", m.sessionPath)
	return true
}

func (m *takeoverMirror) drainQueue() []eventwire.Event {
	m.mu.Lock()
	frames := m.queue.Take(takeoverMirrorMaxQueue)
	m.mu.Unlock()
	return frames
}

func (m *takeoverMirror) requeue(frames []eventwire.Event) {
	if len(frames) == 0 {
		return
	}
	m.mu.Lock()
	m.queue.Prepend(frames)
	m.mu.Unlock()
}

func (m *takeoverMirror) wakeIfQueued() {
	m.mu.Lock()
	pending := m.queue.Len() > 0
	m.mu.Unlock()
	if pending {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

func (m *takeoverMirror) flushOnce(ctx context.Context) {
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	m.flushOnceLocked(ctx)
}

func (m *takeoverMirror) flushOnceLocked(ctx context.Context) {
	if m.returned.Load() {
		return
	}
	client, record, _, grant := m.snapshotClient()
	if client == nil || grant.MirrorID == "" {
		return
	}
	frames := m.drainQueue()
	if len(frames) == 0 {
		return
	}
	marshal := func(batch []eventwire.Event) ([]byte, error) {
		return json.Marshal(map[string]any{"sessionPath": m.sessionPath, "mirrorId": grant.MirrorID, "frames": batch})
	}
	batch, _, payload, err := eventwire.MarshalMirrorBatch(frames, eventwire.MirrorBatchMaxBytes, marshal)
	if err != nil {
		return
	}
	if len(batch) == 0 {
		return
	}
	flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := serveDo(flushCtx, client, http.MethodPost, serveURL(record.base, "/external/frames"), payload)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}
