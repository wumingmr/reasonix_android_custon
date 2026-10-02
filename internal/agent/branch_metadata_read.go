package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	fileencoding "reasonix/internal/fileutil/encoding"
	"reasonix/internal/historywork"
)

// LoadBranchMetaBounded is the content-free catalog reader. An oversized or
// damaged sidecar leaves an unknown metadata row; it never triggers replay.
func LoadBranchMetaBounded(ctx context.Context, sessionPath string) (BranchMeta, bool, error) {
	f, err := os.Open(BranchMetaPath(sessionPath))
	if os.IsNotExist(err) {
		return BranchMeta{}, false, nil
	}
	if err != nil {
		return BranchMeta{}, false, err
	}
	defer f.Close()
	r := &historywork.Reader{Context: ctx, Source: f}
	b, err := io.ReadAll(io.LimitReader(r, historywork.ReadChunk+1))
	if err != nil {
		return BranchMeta{}, false, err
	}
	if len(b) > historywork.ReadChunk {
		return BranchMeta{}, false, fmt.Errorf("session metadata exceeds discovery budget")
	}
	kind, _ := fileencoding.Detect(b)
	return decodeBranchMeta(sessionPath, fileencoding.Decode(b, kind))
}

func decodeBranchMeta(sessionPath string, b []byte) (BranchMeta, bool, error) {
	var m BranchMeta
	if err := json.Unmarshal(b, &m); err != nil {
		// Treat an all-NUL/JSON-whitespace sidecar as a torn write so callers
		// rebuild it; retain errors for partial JSON to avoid swallowing corruption.
		if metaIsUnparseableAsAbsent(b) {
			return BranchMeta{}, false, nil
		}
		return BranchMeta{}, false, fmt.Errorf("decode branch meta %s: %w", BranchMetaPath(sessionPath), err)
	}
	if m.ID == "" {
		m.ID = BranchID(sessionPath)
	}
	m.sanitizeDisplayFields()
	return m, true, nil
}
