package sessioninbox

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"
)

var (
	ErrContentChanged = errors.New("inbox content changed")
	ErrOrderChanged   = errors.New("inbox order changed")
	ErrAnchorMissing  = errors.New("inbox reorder anchor missing")
)

// ContentVersion identifies an immutable body, including an ABA replacement.
// It exposes neither the private blob path nor a new persisted schema field.
func ContentVersion(meta InboxItemMeta) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(meta.ID+"\x00"+blobNameFor(meta)+"\x00"+meta.Checksum)))
}

// UpdateItemIfVersion commits body, reference failure and lifecycle together.
func (s *Store) UpdateItemIfVersion(id string, env PromptEnvelope, version string) (InboxItemMeta, error) {
	if version == "" {
		return InboxItemMeta{}, ErrContentChanged
	}
	return s.updateItem(id, env, "", PromptEnvelope{}, version)
}

// MoveItemBefore changes only pending slots; accepted/running items stay put.
func (s *Store) MoveItemBefore(id string, before *string, revision int64) error {
	if s == nil {
		return ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.beginDiskTransactionLocked()
	if err != nil {
		return err
	}
	defer release()
	if err := s.mutableLocked(); err != nil {
		return err
	}
	if s.man.Revision != revision {
		return ErrOrderChanged
	}
	meta, ok := s.man.item(id)
	if !ok {
		return ErrNotFound
	}
	if !isPendingState(meta.State) {
		return ErrInvalidState
	}
	if before != nil {
		anchor, found := s.man.item(*before)
		if !found || !isPendingState(anchor.State) {
			return ErrAnchorMissing
		}
		if *before == id {
			return nil
		}
	}
	var pending []InboxItemMeta
	var original []string
	for _, item := range s.man.Items {
		if isPendingState(item.State) {
			original = append(original, item.ID)
			if item.ID != id {
				pending = append(pending, item)
			}
		}
	}
	index := len(pending)
	if before != nil {
		index = slices.IndexFunc(pending, func(item InboxItemMeta) bool { return item.ID == *before })
	}
	pending = slices.Insert(pending, index, meta)
	changed := false
	for i := range pending {
		changed = changed || pending[i].ID != original[i]
	}
	if !changed {
		return nil
	}
	next := s.man.clone()
	j := 0
	for i := range next.Items {
		if isPendingState(next.Items[i].State) {
			next.Items[i] = pending[j]
			j++
		}
	}
	if err := s.commitManifestLocked(next); err != nil {
		return err
	}
	s.notifyLocked(s.snapshotLocked())
	return nil
}

// TransitionPrepared validates what was prepared outside the disk transaction.
// A successful edit or move before this boundary must affect actual execution.
func (s *Store) TransitionPrepared(id, version string, target InboxState, reason string, requireHead bool) error {
	if s == nil {
		return ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.beginDiskTransactionLocked()
	if err != nil {
		return err
	}
	defer release()
	if err := s.mutableLocked(); err != nil {
		return err
	}
	meta, ok := s.man.item(id)
	if !ok {
		return ErrNotFound
	}
	if meta.State != StateQueued {
		return ErrInvalidState
	}
	if ContentVersion(meta) != version {
		return ErrContentChanged
	}
	if target != StateBlocked && s.man.Paused {
		return ErrPaused
	}
	if requireHead {
		for _, item := range s.man.Items {
			if isPendingState(item.State) {
				if item.ID != id {
					return ErrOrderChanged
				}
				break
			}
		}
	}
	next := s.man.clone()
	i := next.indexOf(id)
	next.Items[i].State = target
	next.Items[i].BlockReason = reason
	next.Items[i].UpdatedAt = time.Now().UTC()
	if target == StateBlocked {
		next.Paused = true
	}
	if err := s.commitManifestLocked(next); err != nil {
		return err
	}
	s.notifyLocked(s.snapshotLocked())
	return nil
}
