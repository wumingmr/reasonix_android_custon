package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"reasonix/internal/provider"
	"reasonix/internal/store"
)

// MigrationMessageStream is the bounded legacy migration result. Messages is
// the final normalized count after replace records and persisted-safe repairs.
type MigrationMessageStream struct {
	Messages   int
	FromEvents bool
}

// StreamSessionMessagesForMigration emits the authoritative frozen transcript
// without retaining cumulative schema-1/checkpoint history. reset is called
// before the initial stream and whenever a schema-1 replace record supersedes
// its prefix. Schema-2 DAGs still use their graph-specific compatibility
// reader; their disk-index migration is a separate adapter.
func StreamSessionMessagesForMigration(ctx context.Context, path, headID string, reset func() error, emit func(provider.Message) error) (MigrationMessageStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reset == nil || emit == nil {
		return MigrationMessageStream{}, errors.New("session migration stream requires reset and emit callbacks")
	}
	probe, err := probeSessionEventLogWithLimits(path, migrationSessionReplayLimits())
	if err != nil {
		return MigrationMessageStream{}, err
	}
	if probe.futureSchema {
		return MigrationMessageStream{}, fmt.Errorf("session event log for %s uses unsupported schema %d", path, probe.schemaVersion)
	}
	if probe.dag || headID != "" {
		var loaded *Session
		if headID == "" {
			loaded, err = LoadSessionForMigration(ctx, path)
		} else {
			loaded, err = LoadSessionHeadForMigration(ctx, path, headID)
		}
		if err != nil {
			return MigrationMessageStream{}, err
		}
		if err := reset(); err != nil {
			return MigrationMessageStream{}, err
		}
		messages := loaded.Snapshot()
		for _, message := range messages {
			if err := ctx.Err(); err != nil {
				return MigrationMessageStream{}, err
			}
			if err := emit(message); err != nil {
				return MigrationMessageStream{}, err
			}
		}
		return MigrationMessageStream{Messages: len(messages), FromEvents: probe.dag}, nil
	}
	emitter := newMigrationTurnEmitter(path, reset, emit)
	if err := emitter.resetAll(); err != nil {
		return MigrationMessageStream{}, err
	}
	if probe.native && probe.size > 0 {
		records, err := streamSchemaOneMigration(ctx, store.SessionEventLog(path), emitter)
		if err != nil {
			return MigrationMessageStream{FromEvents: true}, err
		}
		if records > 0 {
			if err := emitter.finish(); err != nil {
				return MigrationMessageStream{FromEvents: true}, err
			}
			return MigrationMessageStream{Messages: emitter.count, FromEvents: true}, nil
		}
	}
	if err := emitter.resetAll(); err != nil {
		return MigrationMessageStream{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return MigrationMessageStream{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(&contextReader{ctx: ctx, reader: file})
	for {
		var message provider.Message
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return MigrationMessageStream{}, fmt.Errorf("decode %s: %w", path, err)
		}
		if err := emitter.add(message); err != nil {
			return MigrationMessageStream{}, err
		}
	}
	if err := emitter.finish(); err != nil {
		return MigrationMessageStream{}, err
	}
	return MigrationMessageStream{Messages: emitter.count}, nil
}

type migrationTurnEmitter struct {
	path        string
	reset       func() error
	emit        func(provider.Message) error
	pending     []provider.Message
	runningHash hashWriter
	count       int
	sourceCount int
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
	Reset()
}

func newMigrationTurnEmitter(path string, reset func() error, emit func(provider.Message) error) *migrationTurnEmitter {
	return &migrationTurnEmitter{path: path, reset: reset, emit: emit, runningHash: sha256.New()}
}

func (e *migrationTurnEmitter) resetAll() error {
	e.pending = nil
	e.runningHash.Reset()
	e.count, e.sourceCount = 0, 0
	return e.reset()
}

func (e *migrationTurnEmitter) add(message provider.Message) error {
	// Persisted-safe normalization never pairs work across a real user turn.
	// Buffering one turn preserves receipt/tool repair semantics without
	// retaining the cumulative conversation.
	if message.Role == provider.RoleUser && !message.LocalOnly && len(e.pending) > 0 {
		if err := e.flushTurn(); err != nil {
			return err
		}
	}
	e.pending = append(e.pending, message)
	e.sourceCount++
	return nil
}

func (e *migrationTurnEmitter) finish() error { return e.flushTurn() }

func (e *migrationTurnEmitter) flushTurn() error {
	if len(e.pending) == 0 {
		return nil
	}
	messages := migrateLegacyProviderContent(NormalizeSession(e.pending))
	branchID := BranchID(e.path)
	for _, message := range messages {
		identity, err := json.Marshal(messageForSessionIdentity(message))
		if err != nil {
			return err
		}
		_, _ = e.runningHash.Write(identity)
		_, _ = e.runningHash.Write([]byte{'\n'})
		if message.ID == "" {
			message.ID = legacyMessageID(branchID, e.count, e.runningHash.Sum(nil))
		}
		if err := e.emit(message); err != nil {
			return err
		}
		e.count++
	}
	e.pending = nil
	return nil
}

func streamSchemaOneMigration(ctx context.Context, path string, emitter *migrationTurnEmitter) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	decoder := json.NewDecoder(&contextReader{ctx: ctx, reader: file})
	records := 0
	for {
		if err := ctx.Err(); err != nil {
			return records, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return records, nil
		}
		if err != nil {
			return records, fmt.Errorf("%w: decode schema-1 record: %w", ErrSessionHistoryDamaged, err)
		}
		if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
			return records, fmt.Errorf("%w: schema-1 record is not an object", ErrSessionHistoryDamaged)
		}
		var schema int
		var kind string
		var messageIndex int
		sawMessages := false
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return records, fmt.Errorf("%w: decode schema-1 field: %w", ErrSessionHistoryDamaged, err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return records, fmt.Errorf("%w: invalid schema-1 field name", ErrSessionHistoryDamaged)
			}
			switch name {
			case "schema_version":
				err = decoder.Decode(&schema)
			case "type":
				err = decoder.Decode(&kind)
			case "message_index":
				err = decoder.Decode(&messageIndex)
			case "messages":
				if schema != sessionEventSchemaVersion || (kind != sessionEventTypeReplace && kind != sessionEventTypeAppend) {
					return records, fmt.Errorf("%w: schema/type must precede messages", ErrSessionHistoryDamaged)
				}
				if kind == sessionEventTypeReplace {
					if err := emitter.resetAll(); err != nil {
						return records, err
					}
				} else if messageIndex != emitter.sourceCount {
					return records, fmt.Errorf("%w: append index %d does not match %d", ErrSessionHistoryDamaged, messageIndex, emitter.sourceCount)
				}
				err = decodeMigrationMessageArray(ctx, decoder, emitter)
				sawMessages = true
			default:
				var discard json.RawMessage
				err = decoder.Decode(&discard)
			}
			if err != nil {
				return records, fmt.Errorf("%w: decode schema-1 %s: %w", ErrSessionHistoryDamaged, name, err)
			}
		}
		if _, err := decoder.Token(); err != nil {
			return records, fmt.Errorf("%w: close schema-1 record: %w", ErrSessionHistoryDamaged, err)
		}
		if schema != sessionEventSchemaVersion || !sawMessages {
			return records, fmt.Errorf("%w: incomplete schema-1 record", ErrSessionHistoryDamaged)
		}
		records++
	}
}

func decodeMigrationMessageArray(ctx context.Context, decoder *json.Decoder, emitter *migrationTurnEmitter) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return errors.New("messages must be an array")
	}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var message provider.Message
		if err := decoder.Decode(&message); err != nil {
			return err
		}
		if err := emitter.add(message); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
