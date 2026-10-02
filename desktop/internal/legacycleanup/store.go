package legacycleanup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"reasonix/internal/fileutil"
	filelock "reasonix/internal/identitylock"
)

const SchemaVersion = 1

var (
	ErrNotInitialized     = errors.New("legacy empty session cleanup is not initialized")
	ErrUnsupportedVersion = errors.New("legacy empty session cleanup version is unsupported")
	ErrCorruptState       = errors.New("legacy empty session cleanup state is corrupt")
)

type TopicSnapshot struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	TitleSource   string `json:"titleSource,omitempty"`
	CreatedAt     int64  `json:"createdAt,omitempty"`
	RowRevision   int64  `json:"rowRevision,omitempty"`
	Order         int    `json:"order"`
	Pinned        bool   `json:"pinned,omitempty"`
	GroupID       string `json:"groupId,omitempty"`
	GroupOrder    int    `json:"groupOrder,omitempty"`
}

type SourceSnapshot struct {
	Path        string `json:"path"`
	HeadID      string `json:"headId,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type Candidate struct {
	ID                  string           `json:"id"`
	Kind                string           `json:"kind"`
	WorkspaceID         string           `json:"workspaceId,omitempty"`
	SessionID           string           `json:"sessionId,omitempty"`
	TopicID             string           `json:"topicId,omitempty"`
	SourcePath          string           `json:"sourcePath,omitempty"`
	SourceHeadID        string           `json:"sourceHeadId,omitempty"`
	SourceFingerprint   string           `json:"sourceFingerprint,omitempty"`
	Sources             []SourceSnapshot `json:"sources,omitempty"`
	Title               string           `json:"title"`
	TitleSequence       uint64           `json:"titleSequence,omitempty"`
	EventSequence       uint64           `json:"eventSequence,omitempty"`
	LifecycleGeneration uint64           `json:"lifecycleGeneration,omitempty"`
	OperationID         string           `json:"operationId"`
	Phase               string           `json:"phase"`
	Classification      string           `json:"classification,omitempty"`
	Reason              string           `json:"reason,omitempty"`
	ArchivedAt          int64            `json:"archivedAt,omitempty"`
	Restored            bool             `json:"restored,omitempty"`
	Topic               *TopicSnapshot   `json:"topic,omitempty"`
}

type State struct {
	Version      int                  `json:"version"`
	BatchID      string               `json:"batchId"`
	RegisteredAt time.Time            `json:"registeredAt"`
	Registration string               `json:"registration"`
	Items        map[string]Candidate `json:"items"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store { return &Store{path: filepath.Clean(path)} }

func (s *Store) Path() string {
	if s == nil || s.path == "." {
		return ""
	}
	return s.path
}

// TryAcquireWorker gives one process exclusive ownership of the background
// cleanup pass. Per-update locking protects the sidecar bytes, but it cannot
// prevent two processes from acting on the same candidate between updates.
func (s *Store) TryAcquireWorker() (func(), error) {
	if s == nil || s.Path() == "" {
		return nil, errors.New("legacy cleanup state path is unavailable")
	}
	return filelock.TryAcquire(s.path + ".worker.lock")
}

func (s *Store) Load(ctx context.Context) (State, error) {
	return s.withLock(ctx, false, func() (State, error) { return load(s.path) })
}

func (s *Store) Initialize(ctx context.Context, state State) (State, bool, error) {
	var created bool
	result, err := s.withLock(ctx, true, func() (State, error) {
		current, err := load(s.path)
		if err == nil {
			return current, nil
		}
		if !errors.Is(err, ErrNotInitialized) {
			return State{}, err
		}
		state.Version = SchemaVersion
		state.Registration = "complete"
		if state.RegisteredAt.IsZero() {
			state.RegisteredAt = time.Now().UTC()
		}
		if strings.TrimSpace(state.BatchID) == "" || state.Items == nil {
			return State{}, fmt.Errorf("%w: incomplete initial state", ErrCorruptState)
		}
		if err := save(s.path, state); err != nil {
			return State{}, err
		}
		created = true
		return clone(state)
	})
	return result, created, err
}

func (s *Store) Update(ctx context.Context, mutate func(*State) error) (State, error) {
	return s.withLock(ctx, true, func() (State, error) {
		state, err := load(s.path)
		if err != nil {
			return State{}, err
		}
		if mutate != nil {
			if err := mutate(&state); err != nil {
				return State{}, err
			}
		}
		if err := validate(state); err != nil {
			return State{}, err
		}
		if err := save(s.path, state); err != nil {
			return State{}, err
		}
		return clone(state)
	})
}

// Transition persists a prepare state, runs one external effect while the
// cross-process state lock remains held, then persists the effect outcome.
// The callbacks must not call this Store. Persisting prepare before effect
// makes an interrupted archive distinguishable from one never attempted.
func (s *Store) Transition(ctx context.Context, prepare func(*State) error, effect func() error, finish func(*State, error) error) (State, error) {
	return s.withLock(ctx, true, func() (State, error) {
		state, err := load(s.path)
		if err != nil {
			return State{}, err
		}
		if prepare != nil {
			if err := prepare(&state); err != nil {
				return State{}, err
			}
		}
		if err := validate(state); err != nil {
			return State{}, err
		}
		if err := save(s.path, state); err != nil {
			return State{}, err
		}
		effectErr := error(nil)
		if effect != nil {
			effectErr = effect()
		}
		if finish != nil {
			if err := finish(&state, effectErr); err != nil {
				return State{}, err
			}
		}
		if err := validate(state); err != nil {
			return State{}, err
		}
		if err := save(s.path, state); err != nil {
			return State{}, err
		}
		result, err := clone(state)
		if err != nil {
			return State{}, err
		}
		return result, effectErr
	})
}

func (s *Store) withLock(ctx context.Context, _ bool, fn func() (State, error)) (State, error) {
	if s == nil || s.Path() == "" {
		return State{}, errors.New("legacy cleanup state path is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The advisory lock lives beside the state file, so even a first read needs
	// the parent directory. Creating only the directory and lock file does not
	// initialize or overwrite the versioned state.
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return State{}, err
	}
	release, err := filelock.Acquire(ctx, s.path+".lock")
	if err != nil {
		return State{}, err
	}
	defer release()
	return fn()
}

func load(path string) (State, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, ErrNotInitialized
	}
	if err != nil {
		return State{}, err
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(body, &header); err != nil {
		return State{}, fmt.Errorf("%w: %w", ErrCorruptState, err)
	}
	if header.Version != SchemaVersion {
		return State{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, header.Version)
	}
	var state State
	if err := json.Unmarshal(body, &state); err != nil {
		return State{}, fmt.Errorf("%w: %w", ErrCorruptState, err)
	}
	if err := validate(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func validate(state State) error {
	if state.Version != SchemaVersion || strings.TrimSpace(state.BatchID) == "" || state.Registration != "complete" || state.Items == nil {
		return fmt.Errorf("%w: invalid header", ErrCorruptState)
	}
	for id, item := range state.Items {
		if id == "" || item.ID != id || (item.Kind != "session" && item.Kind != "legacy" && item.Kind != "topic") || item.OperationID == "" || item.Phase == "" {
			return fmt.Errorf("%w: invalid candidate %q", ErrCorruptState, id)
		}
		if item.Kind == "legacy" && strings.TrimSpace(item.SourcePath) == "" {
			return fmt.Errorf("%w: legacy candidate %q has no source", ErrCorruptState, id)
		}
		for _, source := range item.Sources {
			if strings.TrimSpace(source.Path) == "" {
				return fmt.Errorf("%w: candidate %q has an invalid source", ErrCorruptState, id)
			}
		}
	}
	return nil
}

func save(path string, state State) error {
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return fileutil.AtomicWriteFileStrict(path, body, 0o600)
}

func clone(state State) (State, error) {
	body, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	var result State
	if err := json.Unmarshal(body, &result); err != nil {
		return State{}, err
	}
	return result, nil
}

func SortedItems(state State) []Candidate {
	items := make([]Candidate, 0, len(state.Items))
	for _, item := range state.Items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}
