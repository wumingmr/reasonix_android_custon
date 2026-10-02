package main

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"

	"reasonix/internal/config"
	"reasonix/internal/fileutil"
	"reasonix/internal/identitylock"
)

type historicalImportQueueSidecar struct {
	Version       int                                     `json:"version"`
	QueueRevision uint64                                  `json:"queueRevision,omitempty"`
	Current       string                                  `json:"current,omitempty"`
	Queue         []string                                `json:"queue"`
	Presentations map[string]historicalSourcePresentation `json:"presentations,omitempty"`
	extra         map[string]json.RawMessage
}

type historicalSourcePresentation struct {
	Title  string `json:"title,omitempty"`
	Pinned *bool  `json:"pinned,omitempty"`
	extra  map[string]json.RawMessage
}

func (s *historicalImportQueueSidecar) UnmarshalJSON(data []byte) error {
	type plain historicalImportQueueSidecar
	if err := json.Unmarshal(data, (*plain)(s)); err != nil {
		return err
	}
	return json.Unmarshal(data, &s.extra)
}

func (s historicalImportQueueSidecar) MarshalJSON() ([]byte, error) {
	type plain historicalImportQueueSidecar
	return marshalHistoricalFields(plain(s), s.extra, "version", "queueRevision", "current", "queue", "presentations")
}

func (p *historicalSourcePresentation) UnmarshalJSON(data []byte) error {
	type plain historicalSourcePresentation
	if err := json.Unmarshal(data, (*plain)(p)); err != nil {
		return err
	}
	return json.Unmarshal(data, &p.extra)
}

func (p historicalSourcePresentation) MarshalJSON() ([]byte, error) {
	type plain historicalSourcePresentation
	return marshalHistoricalFields(plain(p), p.extra, "title", "pinned")
}

func marshalHistoricalFields(value any, extra map[string]json.RawMessage, known ...string) ([]byte, error) {
	fields := maps.Clone(extra)
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	for _, key := range known {
		delete(fields, key)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func historicalImportQueuePath() string {
	return filepath.Join(filepath.Dir(config.DesktopWorkspaceStatePath()), "historical-import-queue.v1.json")
}

func readHistoricalSidecar() (historicalImportQueueSidecar, error) {
	saved := historicalImportQueueSidecar{Version: 1, Queue: []string{}, Presentations: map[string]historicalSourcePresentation{}}
	data, err := os.ReadFile(historicalImportQueuePath())
	if os.IsNotExist(err) {
		return saved, nil
	}
	if err != nil {
		return saved, err
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return saved, err
	}
	if saved.Version != 1 {
		return saved, errors.New("unsupported historical scheduling version")
	}
	if saved.Presentations == nil {
		saved.Presentations = map[string]historicalSourcePresentation{}
	}
	return saved, nil
}

// The file lock covers read/modify/write, not just atomic replacement. Queue
// writes preserve current presentation fields; presentation writes never alter a batch.
func updateHistoricalSidecar(mutate func(*historicalImportQueueSidecar) error) error {
	path := historicalImportQueuePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	release, err := identitylock.TryAcquire(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	saved, err := readHistoricalSidecar()
	if err != nil {
		return err
	}
	if err := mutate(&saved); err != nil {
		return err
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFileStrict(path, append(data, '\n'), 0o600)
}

func (c *historicalImportCoordinator) loadQueueLocked() {
	c.queueLoaded = true
	saved, err := readHistoricalSidecar()
	if err != nil {
		return
	}
	c.current = ""
	c.queue = append([]string{}, saved.Queue...)
	if saved.Current != "" {
		c.queue = append([]string{saved.Current}, c.queue...)
	}
	c.queueRevision, c.presentations = saved.QueueRevision, saved.Presentations
	c.paused = len(c.queue) > 0
}

func (c *historicalImportCoordinator) saveQueueLocked() error {
	var revision uint64
	err := updateHistoricalSidecar(func(saved *historicalImportQueueSidecar) error {
		if saved.QueueRevision != c.queueRevision {
			return errors.New("historical batch changed in another instance; refresh and retry")
		}
		saved.QueueRevision++
		revision = saved.QueueRevision
		saved.Current, saved.Queue = c.current, append([]string{}, c.queue...)
		return nil
	})
	if err == nil {
		c.queueRevision = revision
	}
	return err
}

func (c *historicalImportCoordinator) claimQueueLocked() error {
	if c.queueRelease != nil {
		return nil
	}
	path := historicalImportQueuePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	release, err := identitylock.TryAcquire(path + ".worker.lock")
	if err != nil {
		return errors.New("historical batch is active in another instance")
	}
	c.queueRelease = release
	c.loadQueueLocked()
	return nil
}

func (c *historicalImportCoordinator) releaseQueueLocked() {
	if c.queueRelease != nil {
		c.queueRelease()
		c.queueRelease = nil
	}
}
