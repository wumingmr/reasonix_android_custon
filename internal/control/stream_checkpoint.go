package control

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// openStreamOutput accumulates the deltas of the stream whose message has not
// committed yet. The streamed text lives nowhere durable until that commit, so
// this is what the autosave tick can checkpoint before a kill.
type openStreamOutput struct {
	mu        sync.Mutex
	messageID string
	text      strings.Builder
	reasoning strings.Builder
	// checkpointed is the text+reasoning length the store already holds.
	checkpointed int
}

func (o *openStreamOutput) observe(e event.Event) {
	if e.Kind == event.TurnStarted {
		o.settle("")
		return
	}
	if (e.Kind != event.Text && e.Kind != event.Reasoning) || e.MessageID == "" || e.Text == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if e.MessageID != o.messageID {
		o.resetLocked(e.MessageID)
	}
	if e.Kind == event.Text {
		o.text.WriteString(e.Text)
	} else {
		o.reasoning.WriteString(e.Text)
	}
}

// settle forgets the stream once its message is committed; an empty id
// forgets whatever stream is open. Any other commit leaves the stream open but
// supersedes its stored checkpoint, so the next tick writes it again.
func (o *openStreamOutput) settle(committedID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if committedID == "" || committedID == o.messageID {
		o.resetLocked("")
		return
	}
	o.checkpointed = 0
}

func (o *openStreamOutput) resetLocked(messageID string) {
	o.messageID = messageID
	o.text.Reset()
	o.reasoning.Reset()
	o.checkpointed = 0
}

func (o *openStreamOutput) pending() (provider.Message, int, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	size := o.text.Len() + o.reasoning.Len()
	if o.messageID == "" || size == 0 || size == o.checkpointed {
		return provider.Message{}, 0, false
	}
	return agent.InterruptedStreamRecord(o.messageID, o.text.String(), o.reasoning.String()), size, true
}

func (o *openStreamOutput) markCheckpointed(messageID string, size int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.messageID == messageID {
		o.checkpointed = size
	}
}

// settleOpenStreamLocked runs under commitMu with the messages just committed.
func (c *Controller) settleOpenStreamLocked(messages []provider.Message) {
	for _, m := range messages {
		c.turnEvents.openStream.settle(m.ID)
	}
}

// checkpointOpenStream records the open stream's output as the local-only
// record its interruption would leave. It holds commitMu so the checkpoint can
// never land after the commit of the message it describes.
func (c *Controller) checkpointOpenStream(ctx context.Context) error {
	store := c.sessionEventStore()
	if store == nil || !c.sessionEventCommitAllowed() {
		return nil
	}
	c.turnEvents.commitMu.Lock()
	defer c.turnEvents.commitMu.Unlock()
	if !c.messageCommitAllowedLocked(ctx, store) {
		return nil
	}
	record, size, ok := c.turnEvents.openStream.pending()
	if !ok || c.turnEvents.turnMessageIDs[record.ID] {
		return nil
	}
	turnID := store.StateSnapshot().Projection.TurnID
	if turnID == "" {
		return nil
	}
	checkpoint, err := session.StreamCheckpointEvent(record)
	if err != nil {
		return err
	}
	op := fmt.Sprintf("stream-checkpoint:%s:%d", record.ID, size)
	if _, err := c.appendSessionBatch(ctx, store, session.Batch{OperationID: op, TurnID: turnID, Events: []session.Event{checkpoint}}); err != nil {
		return err
	}
	c.turnEvents.openStream.markCheckpointed(record.ID, size)
	return nil
}
