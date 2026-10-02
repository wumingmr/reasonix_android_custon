package session

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"reasonix/internal/sessioncontent"
)

const (
	V4SchemaVersion      = 4
	V4Codec              = "reasonix.session.linear/v4"
	v4InlinePayloadBytes = 64 << 10
	v4MaxFrameBytes      = 8 << 20
	v4FrameHeaderBytes   = 12
)

var v4FrameMagic = [4]byte{'R', 'X', '4', 'F'}

type v4Record struct {
	SchemaVersion int    `json:"schemaVersion"`
	Codec         string `json:"codec"`
	RecordType    string `json:"recordType"`

	CommitID         string    `json:"commitId,omitempty"`
	OperationID      string    `json:"operationId,omitempty"`
	OperationHash    string    `json:"operationHash,omitempty"`
	FirstSequence    uint64    `json:"firstSeq,omitempty"`
	EventCount       int       `json:"eventCount,omitempty"`
	TurnID           string    `json:"turnId,omitempty"`
	WriterGeneration uint64    `json:"writerGeneration,omitempty"`
	CreatedAt        time.Time `json:"createdAt,omitempty"`

	Event  *v4Event `json:"event,omitempty"`
	SHA256 string   `json:"sha256,omitempty"`
}

type v4Event struct {
	ID         string              `json:"id"`
	Sequence   uint64              `json:"seq"`
	Kind       string              `json:"kind"`
	Optional   bool                `json:"optional,omitempty"`
	Required   bool                `json:"required,omitempty"`
	Payload    []byte              `json:"payload,omitempty"`
	PayloadRef *sessioncontent.Ref `json:"payloadRef,omitempty"`
}

// encodeV4Commits writes each logical commit as an atomic begin/event/end
// transaction. Every record is an independent checksummed Zstandard frame.
// Large payload bytes are durably published to content before their reference
// can enter the log.
func encodeV4Commits(ctx context.Context, dst io.Writer, content *sessioncontent.Store, commits []Commit) ([]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoder, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedFastest),
		zstd.WithWindowSize(1<<20),
		zstd.WithEncoderCRC(true),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		return nil, fmt.Errorf("sessionv4: create encoder: %w", err)
	}
	defer encoder.Close()
	lengths := make([]int64, 0, len(commits))
	for _, commit := range commits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		counting := &countingWriter{writer: dst}
		digest := sha256.New()
		begin := v4Record{
			SchemaVersion: V4SchemaVersion, Codec: V4Codec, RecordType: "batch/begin",
			CommitID: commit.ID, OperationID: commit.OperationID, OperationHash: commit.OperationHash,
			FirstSequence: commit.FirstSequence, EventCount: commit.EventCount, TurnID: commit.TurnID,
			WriterGeneration: commit.WriterGeneration, CreatedAt: commit.CreatedAt,
		}
		if err := writeV4DigestRecord(ctx, counting, encoder, digest, begin); err != nil {
			return nil, err
		}
		for _, event := range commit.Events {
			recordEvent := &v4Event{
				ID: event.ID, Sequence: event.Sequence, Kind: event.Kind,
				Optional: event.Optional, Required: event.Required,
			}
			if event.PayloadRef != nil {
				if len(event.Payload) != 0 || content == nil {
					return nil, errors.New("sessionv4: referenced payload must have one available content store")
				}
				if err := content.Verify(ctx, *event.PayloadRef); err != nil {
					return nil, fmt.Errorf("sessionv4: verify event %s payload: %w", event.ID, err)
				}
				ref := *event.PayloadRef
				recordEvent.PayloadRef = &ref
			} else if len(event.Payload) > v4InlinePayloadBytes {
				if content == nil {
					return nil, errors.New("sessionv4: content store is required for a large payload")
				}
				ref, putErr := content.Put(ctx, bytesReader(event.Payload), sessioncontent.Metadata{MediaType: "application/json"})
				if putErr != nil {
					return nil, fmt.Errorf("sessionv4: store event %s payload: %w", event.ID, putErr)
				}
				recordEvent.PayloadRef = &ref
			} else {
				recordEvent.Payload = append([]byte(nil), event.Payload...)
			}
			if err := writeV4DigestRecord(ctx, counting, encoder, digest, v4Record{
				SchemaVersion: V4SchemaVersion, Codec: V4Codec, RecordType: "batch/event", Event: recordEvent,
			}); err != nil {
				return nil, err
			}
		}
		end := v4Record{
			SchemaVersion: V4SchemaVersion, Codec: V4Codec, RecordType: "batch/end",
			CommitID: commit.ID, FirstSequence: commit.FirstSequence, EventCount: commit.EventCount,
			SHA256: hex.EncodeToString(digest.Sum(nil)),
		}
		if err := writeV4Record(ctx, counting, encoder, end); err != nil {
			return nil, err
		}
		lengths = append(lengths, counting.count)
	}
	return lengths, nil
}

func writeV4DigestRecord(ctx context.Context, dst io.Writer, encoder *zstd.Encoder, digest hash.Hash, record v4Record) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := digest.Write(raw); err != nil {
		return err
	}
	if _, err := digest.Write([]byte{0}); err != nil {
		return err
	}
	return writeV4RawRecord(ctx, dst, encoder, raw)
}

func writeV4Record(ctx context.Context, dst io.Writer, encoder *zstd.Encoder, record v4Record) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeV4RawRecord(ctx, dst, encoder, raw)
}

func writeV4RawRecord(ctx context.Context, dst io.Writer, encoder *zstd.Encoder, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > v4MaxFrameBytes {
		return fmt.Errorf("sessionv4: physical record size %d exceeds frame budget %d", len(raw), v4MaxFrameBytes)
	}
	compressed := encoder.EncodeAll(raw, nil)
	if len(compressed) == 0 || len(compressed) > v4MaxFrameBytes {
		return fmt.Errorf("sessionv4: compressed frame size %d exceeds frame budget %d", len(compressed), v4MaxFrameBytes)
	}
	var header [v4FrameHeaderBytes]byte
	copy(header[:4], v4FrameMagic[:])
	binary.BigEndian.PutUint32(header[4:8], uint32(len(compressed)))
	binary.BigEndian.PutUint32(header[8:12], uint32(len(raw)))
	if err := writeAllContext(ctx, dst, header[:]); err != nil {
		return err
	}
	return writeAllContext(ctx, dst, compressed)
}

// scanV4CommitFile validates transaction framing and exposes only batches
// whose end record and digest are complete. A partial final frame or final
// batch is an uncommitted tail and therefore invisible.
func scanV4CommitFile(ctx context.Context, file io.ReadSeeker, startOffset int64, nextSequence uint64, content *sessioncontent.Store, knownKinds map[string]bool, visit func(int64, Commit) bool) error {
	return scanV4CommitFileMode(ctx, file, startOffset, nextSequence, content, knownKinds, true, visit)
}

// scanV4CommitFileRefs validates the same durable transaction stream while
// leaving external payloads as references. Index rebuilds and history queries
// use this path so cumulative history is never materialized merely to locate
// records.
func scanV4CommitFileRefs(ctx context.Context, file io.ReadSeeker, startOffset int64, nextSequence uint64, content *sessioncontent.Store, knownKinds map[string]bool, visit func(int64, Commit) bool) error {
	return scanV4CommitFileMode(ctx, file, startOffset, nextSequence, content, knownKinds, false, visit)
}

func scanV4CommitFileMode(ctx context.Context, file io.ReadSeeker, startOffset int64, nextSequence uint64, content *sessioncontent.Store, knownKinds map[string]bool, resolvePayloads bool, visit func(int64, Commit) bool) error {
	if knownKinds == nil {
		knownKinds = ProjectionKinds
	}
	if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
		return err
	}
	decoder, err := zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(v4MaxFrameBytes),
	)
	if err != nil {
		return fmt.Errorf("sessionv4: create decoder: %w", err)
	}
	defer decoder.Close()

	var pending *Commit
	var pendingOffset int64
	var digest hash.Hash
	offset := startOffset
	operations := map[string]string{}
	for {
		recordOffset := offset
		raw, frameBytes, complete, readErr := readV4Frame(ctx, file, decoder)
		if readErr != nil {
			return readErr
		}
		if !complete {
			return nil
		}
		offset += frameBytes
		var record v4Record
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("%w: decode v4 record at %d: %w", ErrDamagedStore, recordOffset, err)
		}
		if record.SchemaVersion != V4SchemaVersion || record.Codec != V4Codec {
			return fmt.Errorf("%w: v4 physical record at %d", ErrUnsupportedVersion, recordOffset)
		}
		switch record.RecordType {
		case "batch/begin":
			if pending != nil {
				return fmt.Errorf("%w: nested v4 batch at %d", ErrDamagedStore, recordOffset)
			}
			if record.CommitID == "" || record.OperationID == "" || record.OperationHash == "" || record.WriterGeneration == 0 || record.FirstSequence != nextSequence || record.EventCount <= 0 {
				return fmt.Errorf("%w: invalid v4 batch boundary at sequence %d", ErrDamagedStore, nextSequence)
			}
			pending = &Commit{
				SchemaVersion: V4SchemaVersion, Codec: V4Codec, RecordType: "commit",
				ID: record.CommitID, OperationID: record.OperationID, OperationHash: record.OperationHash,
				FirstSequence: record.FirstSequence, EventCount: record.EventCount, TurnID: record.TurnID,
				WriterGeneration: record.WriterGeneration, CreatedAt: record.CreatedAt,
				// Never trust an on-disk cumulative count as an allocation request.
				// Capacity grows only as individually bounded frames validate.
				Events: nil,
			}
			pendingOffset = recordOffset
			digest = sha256.New()
			_, _ = digest.Write(raw)
			_, _ = digest.Write([]byte{0})
		case "batch/event":
			if pending == nil || record.Event == nil || len(pending.Events) >= pending.EventCount {
				return fmt.Errorf("%w: v4 event outside batch at %d", ErrDamagedStore, recordOffset)
			}
			physical := record.Event
			wantSequence := pending.FirstSequence + uint64(len(pending.Events))
			if physical.ID == "" || strings.TrimSpace(physical.Kind) == "" || physical.Sequence != wantSequence || (physical.PayloadRef != nil && physical.Payload != nil) {
				return fmt.Errorf("%w: invalid v4 event at sequence %d", ErrDamagedStore, wantSequence)
			}
			payload := append(json.RawMessage(nil), physical.Payload...)
			var payloadRef *sessioncontent.Ref
			if physical.PayloadRef != nil {
				ref := *physical.PayloadRef
				payloadRef = &ref
				if resolvePayloads {
					payload, err = resolveContentPayload(ctx, content, ref)
					if err != nil {
						return fmt.Errorf("%w: read v4 event %s payload: %w", ErrDamagedStore, physical.ID, err)
					}
					payloadRef = nil
				}
			}
			pending.Events = append(pending.Events, Event{
				ID: physical.ID, Sequence: physical.Sequence, Kind: physical.Kind,
				Optional: physical.Optional, Required: physical.Required, Payload: payload, PayloadRef: payloadRef,
			})
			_, _ = digest.Write(raw)
			_, _ = digest.Write([]byte{0})
		case "batch/end":
			if pending == nil || record.CommitID != pending.ID || record.FirstSequence != pending.FirstSequence || record.EventCount != pending.EventCount || len(pending.Events) != pending.EventCount {
				return fmt.Errorf("%w: invalid v4 batch end at %d", ErrDamagedStore, recordOffset)
			}
			if got := hex.EncodeToString(digest.Sum(nil)); record.SHA256 != got {
				return fmt.Errorf("%w: v4 batch %s checksum is %s, expected %s", ErrDamagedStore, pending.ID, record.SHA256, got)
			}
			if prior, ok := operations[pending.OperationID]; ok && prior != pending.OperationHash {
				return fmt.Errorf("%w: conflicting operation %q", ErrDamagedStore, pending.OperationID)
			}
			operations[pending.OperationID] = pending.OperationHash
			for _, event := range pending.Events {
				if !event.Optional && !knownKinds[event.Kind] {
					return fmt.Errorf("%w: unknown required event %q", ErrUnsupportedVersion, event.Kind)
				}
			}
			nextSequence = pending.LastSequence() + 1
			completed := *pending
			pending = nil
			digest = nil
			if visit != nil && !visit(pendingOffset, completed) {
				return nil
			}
		default:
			return fmt.Errorf("%w: unknown v4 physical record %q", ErrUnsupportedVersion, record.RecordType)
		}
	}
}

func resolveContentPayload(ctx context.Context, content *sessioncontent.Store, ref sessioncontent.Ref) (json.RawMessage, error) {
	if content == nil {
		return nil, errors.New("sessionv4: content store is required to resolve a payload reference")
	}
	if ref.Bytes < 0 || uint64(ref.Bytes) > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("sessionv4: payload size %d cannot be materialized by this process", ref.Bytes)
	}
	r, err := content.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	payload := make([]byte, int(ref.Bytes))
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func readV4Frame(ctx context.Context, reader io.Reader, decoder *zstd.Decoder) ([]byte, int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	var header [v4FrameHeaderBytes]byte
	n, err := io.ReadFull(reader, header[:])
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, int64(n), false, nil
	}
	if err != nil {
		return nil, int64(n), false, err
	}
	if [4]byte(header[:4]) != v4FrameMagic {
		return nil, 0, false, fmt.Errorf("%w: invalid v4 frame magic", ErrDamagedStore)
	}
	compressedBytes := int(binary.BigEndian.Uint32(header[4:8]))
	rawBytes := int(binary.BigEndian.Uint32(header[8:12]))
	if compressedBytes <= 0 || compressedBytes > v4MaxFrameBytes || rawBytes <= 0 || rawBytes > v4MaxFrameBytes {
		return nil, 0, false, fmt.Errorf("%w: invalid v4 frame sizes compressed=%d raw=%d", ErrDamagedStore, compressedBytes, rawBytes)
	}
	compressed := make([]byte, compressedBytes)
	n, err = io.ReadFull(reader, compressed)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, int64(v4FrameHeaderBytes + n), false, nil
	}
	if err != nil {
		return nil, int64(v4FrameHeaderBytes + n), false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	raw, err := decoder.DecodeAll(compressed, make([]byte, 0, rawBytes))
	if err != nil {
		return nil, 0, false, fmt.Errorf("%w: decode v4 frame: %w", ErrDamagedStore, err)
	}
	if len(raw) != rawBytes {
		return nil, 0, false, fmt.Errorf("%w: v4 frame decoded %d bytes, expected %d", ErrDamagedStore, len(raw), rawBytes)
	}
	return raw, int64(v4FrameHeaderBytes + compressedBytes), true, nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.count += int64(n)
	return n, err
}

type rawBytesReader struct {
	data []byte
	off  int
}

func bytesReader(data []byte) *rawBytesReader { return &rawBytesReader{data: data} }

func (r *rawBytesReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
