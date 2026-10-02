// Command sessioncapacity exercises the production session service at a
// configurable scale. It intentionally keeps acceptance sizes in flags rather
// than production constants: 100k events and GiB-sized data sets are release
// evidence, not product limits.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/sessioncontent"
)

const mib = int64(1 << 20)

type config struct {
	Root                 string
	SessionID            string
	HistoryMessages      int
	HistoryBytes         int64
	AttachmentBytes      int64
	AttachmentChunkBytes int64
	WorksetBytes         int64
	FlushEvery           int
	PageSamples          int
}

type report struct {
	SessionID               string `json:"sessionId"`
	HistoryMessages         int    `json:"historyMessages"`
	HistoryLogicalBytes     int64  `json:"historyLogicalBytes"`
	AttachmentObjects       int    `json:"attachmentObjects"`
	AttachmentLogicalBytes  int64  `json:"attachmentLogicalBytes"`
	EventSequence           uint64 `json:"eventSequence"`
	DurableSequence         uint64 `json:"durableSequence"`
	ModelWorksetBytes       int64  `json:"modelWorksetBytes"`
	WriteDurationMS         int64  `json:"writeDurationMs"`
	ColdOpenDurationMS      int64  `json:"coldOpenDurationMs"`
	HistoryIndexBuildMS     int64  `json:"historyIndexBuildDurationMs"`
	IndexedPageP95MS        int64  `json:"indexedPageP95Ms"`
	SessionDiskBytes        int64  `json:"sessionDiskBytes"`
	ContentDiskBytes        int64  `json:"contentDiskBytes"`
	QueryCacheDiskBytes     int64  `json:"queryCacheDiskBytes"`
	BaselineHeapAllocBytes  uint64 `json:"baselineHeapAllocBytes"`
	PeakHeapAllocBytes      uint64 `json:"peakHeapAllocBytes"`
	BaselineRuntimeSysBytes uint64 `json:"baselineRuntimeSysBytes"`
	PeakRuntimeSysBytes     uint64 `json:"peakRuntimeSysBytes"`
	ProcessPeakRSSBytes     uint64 `json:"processPeakRssBytes,omitempty"`
	WritePeakHeapBytes      uint64 `json:"writePeakHeapBytes"`
	ColdOpenPeakHeapBytes   uint64 `json:"coldOpenPeakHeapBytes"`
	HistoryPeakHeapBytes    uint64 `json:"historyPeakHeapBytes"`
}

func main() {
	var cfg config
	flag.StringVar(&cfg.Root, "root", "", "sessions-v4 root (a temporary root is used when empty)")
	flag.StringVar(&cfg.SessionID, "session", "capacity-acceptance", "session identity")
	flag.IntVar(&cfg.HistoryMessages, "history-messages", 2_000, "number of durable history messages")
	flag.Int64Var(&cfg.HistoryBytes, "history-bytes", 32*mib, "logical bytes spread across history messages")
	flag.Int64Var(&cfg.AttachmentBytes, "attachment-bytes", 32*mib, "bytes streamed through the shared content store")
	flag.Int64Var(&cfg.AttachmentChunkBytes, "attachment-chunk-bytes", 4*mib, "maximum bytes per attachment object")
	flag.Int64Var(&cfg.WorksetBytes, "workset-bytes", 4*mib, "provider model workset retained across cold open")
	flag.IntVar(&cfg.FlushEvery, "flush-every", 128, "flush interval in history messages")
	flag.IntVar(&cfg.PageSamples, "page-samples", 7, "indexed newest-page timing samples")
	flag.Parse()

	rootWasTemporary := cfg.Root == ""
	if rootWasTemporary {
		temp, err := os.MkdirTemp("", "reasonix-session-capacity-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cfg.Root = filepath.Join(temp, "sessions-v4")
		defer os.RemoveAll(temp)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	result, err := run(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
	if !rootWasTemporary {
		fmt.Fprintf(os.Stderr, "capacity data retained at %s\n", cfg.Root)
	}
}

// releaseCapacityService drops the query cache together with the writer lease
// and recovery store. Closing the query alone leaves recovery-v1.bolt open, and
// Windows refuses to remove a directory that still holds it.
func releaseCapacityService(service *session.Service) {
	service.Query().Close()
	_ = service.CloseAll(context.Background())
}

func run(ctx context.Context, cfg config) (result report, err error) {
	if err := validateConfig(cfg); err != nil {
		return report{}, err
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return report{}, err
	}
	runtime.GC()
	baseline := readMemory()
	peaks := newMemoryPeaks(baseline)
	stopMemory := make(chan struct{})
	memoryDone := make(chan struct{})
	go sampleMemory(stopMemory, memoryDone, peaks)
	defer func() {
		close(stopMemory)
		<-memoryDone
		peaks.observe(readMemory())
		peak := peaks.totalPeak()
		result.BaselineHeapAllocBytes = baseline.heap
		result.PeakHeapAllocBytes = peak.heap
		result.BaselineRuntimeSysBytes = baseline.sys
		result.PeakRuntimeSysBytes = peak.sys
		result.ProcessPeakRSSBytes = peak.rss
	}()

	service, err := session.NewService("capacity", session.NewFilesystemPersistence(cfg.Root))
	if err != nil {
		return report{}, err
	}
	runtimeSession, err := service.Create(ctx, session.CreateOptions{SessionID: cfg.SessionID})
	if err != nil {
		return report{}, err
	}
	ref := runtimeSession.Ref()
	defer service.Query().Close()

	writeStarted := time.Now()
	written, err := appendHistory(ctx, runtimeSession.Session(), cfg)
	if err != nil {
		_ = service.Close(context.Background(), ref)
		return report{}, err
	}
	attachments, err := writeAttachments(ctx, filepath.Join(cfg.Root, ".content-v1"), cfg.AttachmentBytes, cfg.AttachmentChunkBytes)
	if err != nil {
		_ = service.Close(context.Background(), ref)
		return report{}, err
	}
	workset := provider.Message{ID: "capacity-workset", Role: provider.RoleUser, Content: deterministicString(cfg.WorksetBytes, uint64(cfg.HistoryMessages)+1)}
	worksetPayload, err := json.Marshal(map[string]any{"messages": []provider.Message{workset}, "reason": "capacity acceptance workset"})
	if err != nil {
		return report{}, err
	}
	if _, err := runtimeSession.Session().AppendBatch(ctx, "capacity-workset", []session.Event{{Kind: "model/context-replace", Payload: worksetPayload}}); err != nil {
		return report{}, err
	}
	receipt, err := runtimeSession.Session().Flush(ctx)
	if err != nil {
		return report{}, err
	}
	result.WriteDurationMS = time.Since(writeStarted).Milliseconds()
	result.SessionID = cfg.SessionID
	result.HistoryMessages = cfg.HistoryMessages
	result.HistoryLogicalBytes = written
	result.AttachmentObjects = attachments
	result.AttachmentLogicalBytes = cfg.AttachmentBytes
	result.EventSequence = runtimeSession.Session().EventSequence()
	result.DurableSequence = receipt.DurableSequence
	if err := service.Close(ctx, ref); err != nil {
		return report{}, err
	}
	result.WritePeakHeapBytes = peaks.nextStage().heap

	second, err := session.NewService("capacity", session.NewFilesystemPersistence(cfg.Root))
	if err != nil {
		return report{}, err
	}
	defer releaseCapacityService(second)
	openStarted := time.Now()
	binding, err := second.Open(ctx, ref)
	if err != nil {
		return report{}, err
	}
	result.ColdOpenDurationMS = time.Since(openStarted).Milliseconds()
	model := binding.Runtime().Session().DeriveMessages()
	for _, message := range model {
		result.ModelWorksetBytes += int64(len(message.Content))
	}
	if result.ModelWorksetBytes != cfg.WorksetBytes {
		_ = binding.Release(context.Background())
		return report{}, fmt.Errorf("capacity: cold model workset is %d bytes, expected %d", result.ModelWorksetBytes, cfg.WorksetBytes)
	}
	result.ColdOpenPeakHeapBytes = peaks.nextStage().heap

	indexStarted := time.Now()
	page, err := waitForHistoryPage(ctx, second.Query(), ref)
	if err != nil {
		_ = binding.Release(context.Background())
		return report{}, err
	}
	result.HistoryIndexBuildMS = time.Since(indexStarted).Milliseconds()
	if len(page.Messages) == 0 && cfg.HistoryMessages > 0 {
		_ = binding.Release(context.Background())
		return report{}, errors.New("capacity: rebuilt history index returned no messages")
	}
	durations := make([]time.Duration, 0, cfg.PageSamples)
	for range cfg.PageSamples {
		started := time.Now()
		if _, err := second.Query().HistoryPage(ctx, ref, "", 100); err != nil {
			_ = binding.Release(context.Background())
			return report{}, err
		}
		durations = append(durations, time.Since(started))
	}
	result.IndexedPageP95MS = percentile95(durations).Milliseconds()
	if err := binding.Release(ctx); err != nil {
		return report{}, err
	}
	result.HistoryPeakHeapBytes = peaks.nextStage().heap

	result.SessionDiskBytes, err = treeBytes(filepath.Join(cfg.Root, cfg.SessionID))
	if err != nil {
		return report{}, err
	}
	result.ContentDiskBytes, err = treeBytes(filepath.Join(cfg.Root, ".content-v1"))
	if err != nil {
		return report{}, err
	}
	result.QueryCacheDiskBytes, err = treeBytes(filepath.Join(cfg.Root, ".query-cache"))
	return result, err
}

func waitForHistoryPage(ctx context.Context, query *session.Query, ref session.SessionRef) (session.MessageHistoryPage, error) {
	for {
		page, err := query.HistoryPage(ctx, ref, "", 100)
		if err != nil || page.Status == "ready" {
			return page, err
		}
		if page.Status != "preparing" {
			return session.MessageHistoryPage{}, fmt.Errorf("capacity: history locator status %q", page.Status)
		}
		select {
		case <-ctx.Done():
			return session.MessageHistoryPage{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func validateConfig(cfg config) error {
	if strings.TrimSpace(cfg.Root) == "" || strings.TrimSpace(cfg.SessionID) == "" {
		return errors.New("capacity: root and session are required")
	}
	if cfg.HistoryMessages < 1 || cfg.HistoryBytes < 0 || cfg.AttachmentBytes < 0 || cfg.AttachmentChunkBytes < 1 || cfg.WorksetBytes < 0 {
		return errors.New("capacity: sizes must be non-negative and history/chunk counts must be positive")
	}
	if cfg.FlushEvery < 1 || cfg.PageSamples < 1 {
		return errors.New("capacity: flush interval and page samples must be positive")
	}
	return nil
}

func appendHistory(ctx context.Context, target *session.Session, cfg config) (int64, error) {
	emptyContext := json.RawMessage(`{"messages":[],"reason":"capacity bounded workset"}`)
	var written int64
	for i := range cfg.HistoryMessages {
		remainingMessages := int64(cfg.HistoryMessages - i)
		contentBytes := (cfg.HistoryBytes - written + remainingMessages - 1) / remainingMessages
		content := deterministicString(contentBytes, uint64(i)+1)
		message := provider.Message{ID: fmt.Sprintf("capacity-%08d", i), Role: provider.RoleUser, Content: content}
		payload, err := json.Marshal(map[string]any{"message": message})
		if err != nil {
			return written, err
		}
		_, err = target.AppendBatch(ctx, fmt.Sprintf("capacity-%08d", i), []session.Event{
			{Kind: "message/complete", Payload: payload},
			{Kind: "model/context-replace", Payload: emptyContext},
		})
		if err != nil {
			return written, err
		}
		written += contentBytes
		if (i+1)%cfg.FlushEvery == 0 {
			if _, err := target.Flush(ctx); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

func writeAttachments(ctx context.Context, root string, total, chunk int64) (int, error) {
	store := sessioncontent.New(root)
	objects := 0
	for offset := int64(0); offset < total; {
		size := min(chunk, total-offset)
		reader := io.LimitReader(&deterministicReader{state: uint64(objects) + 0x9e3779b97f4a7c15}, size)
		ref, err := store.Put(ctx, reader, sessioncontent.Metadata{MediaType: "application/octet-stream", Name: fmt.Sprintf("capacity-%06d.bin", objects)})
		if err != nil {
			return objects, err
		}
		if ref.Bytes != size {
			return objects, fmt.Errorf("capacity: attachment %d stored %d bytes, expected %d", objects, ref.Bytes, size)
		}
		offset += size
		objects++
	}
	return objects, nil
}

func deterministicString(size int64, seed uint64) string {
	if size <= 0 {
		return ""
	}
	buf := make([]byte, int(size))
	r := deterministicReader{state: seed + 0x9e3779b97f4a7c15}
	_, _ = io.ReadFull(&r, buf)
	return string(buf)
}

type deterministicReader struct{ state uint64 }

func (r *deterministicReader) Read(p []byte) (int, error) {
	for i := range p {
		r.state ^= r.state << 13
		r.state ^= r.state >> 7
		r.state ^= r.state << 17
		p[i] = byte(33 + r.state%90)
	}
	return len(p), nil
}

type memorySample struct{ heap, sys, rss uint64 }

type memoryPeaks struct {
	mu    sync.Mutex
	stage memorySample
	total memorySample
}

func newMemoryPeaks(initial memorySample) *memoryPeaks {
	return &memoryPeaks{stage: initial, total: initial}
}

func (p *memoryPeaks) observe(sample memorySample) {
	p.mu.Lock()
	p.stage.max(sample)
	p.total.max(sample)
	p.mu.Unlock()
}

func (p *memoryPeaks) nextStage() memorySample {
	latest := readMemory()
	p.mu.Lock()
	p.stage.max(latest)
	p.total.max(latest)
	finished := p.stage
	p.stage = latest
	p.mu.Unlock()
	return finished
}

func (p *memoryPeaks) totalPeak() memorySample {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total
}

func readMemory() memorySample {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return memorySample{heap: stats.HeapAlloc, sys: stats.Sys, rss: processPeakRSSBytes()}
}

func (m *memorySample) max(other memorySample) {
	m.heap = max(m.heap, other.heap)
	m.sys = max(m.sys, other.sys)
	m.rss = max(m.rss, other.rss)
}

func sampleMemory(stop <-chan struct{}, done chan<- struct{}, peaks *memoryPeaks) {
	defer close(done)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			peaks.observe(readMemory())
		}
	}
}

func percentile95(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	copyOfValues := append([]time.Duration(nil), values...)
	slices.Sort(copyOfValues)
	index := (95*len(copyOfValues) + 99) / 100
	return copyOfValues[index-1]
}

func treeBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
