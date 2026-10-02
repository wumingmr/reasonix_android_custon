package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"reasonix/desktop/internal/browserops"
	"reasonix/internal/config"
	"reasonix/internal/extension/rpcwire"
)

func browserDiagnosticScope(hostID, sessionID string) string {
	if hostID == "" || sessionID == "" {
		return ""
	}
	data, _ := json.Marshal([]string{hostID, sessionID})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (a *App) browserDiagnosticExport(job *sessionExportJob) map[string]any {
	out := map[string]any{
		"schemaVersion": 1, "capturedAt": time.Now().UTC(), "source": "desktop",
		"toolEvidence": "Browser tool calls, results and errors are in the session commits; their prefix cutoff can precede this runtime capture.",
		"coverage":     "Host/page events are a bounded in-memory window, lost on shell restart. This is not a complete browsing history. Remote sessions include only the browser hosted by this desktop; remote CDP/browser logs are not collected. Legacy unattributed records are omitted.",
	}
	if job.browserScope == "" {
		out["unavailable"] = "No fixed browser diagnostic identity for this session."
		return out
	}
	a.browserExecMu.Lock()
	ledger := a.browserOps
	a.browserExecMu.Unlock()
	var operations browserops.DiagnosticSnapshot
	var err error
	if ledger != nil {
		operations = ledger.Diagnostics(job.browserScope)
	} else {
		operations, err = browserops.ReadDiagnostics(filepath.Join(config.MemoryUserDir(), "browser", "operations-v1.json"), job.browserScope)
	}
	if err != nil {
		out["operations"] = map[string]any{"available": false, "reason": "Operation ledger is absent, unreadable, unsupported or exceeds the read budget."}
	} else {
		out["operations"] = operations
	}
	var host hostRequester
	if a.hostShell != nil && a.hostShell.server != nil {
		host = a.hostShell.server
	}
	out["host"] = captureBrowserHostDiagnostics(job.ctx, host, job.browserScope)
	return out
}

func captureBrowserHostDiagnostics(ctx context.Context, host hostRequester, scope string) any {
	unavailable := func(kind, reason string) any {
		return map[string]any{"available": false, "kind": kind, "reason": reason}
	}
	if host == nil || scope == "" {
		return unavailable("host_unavailable", "Browser diagnostic host or fixed session identity is unavailable.")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := host.Request(ctx, "host/browser.exportDiagnostics", map[string]string{"scope": scope}, &raw); err != nil {
		if ctx.Err() != nil {
			return unavailable("capture_interrupted", "Browser diagnostic capture was cancelled or exceeded its three-second deadline.")
		}
		var response *rpcwire.ResponseError
		if errors.As(err, &response) && response.Code == -32601 {
			return unavailable("capability_unsupported", "This shell does not support browser diagnostic export.")
		}
		return unavailable("host_request_failed", "Browser diagnostic host failed or disconnected; no browser action was replayed.")
	}
	if len(raw) > 512<<10 || !json.Valid(raw) {
		return unavailable("invalid_response", "Invalid or oversized host diagnostic response.")
	}
	var header struct {
		Scope     string `json:"scope"`
		Available bool   `json:"available"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Scope != scope || !header.Available {
		return unavailable("identity_unverified", "Host diagnostic response is unavailable or its session identity does not match.")
	}
	return raw
}

// The remote endpoint intentionally accepts only its original observation
// fields. Append desktop evidence locally, preserving its streaming document
// and compatibility with older remote services (and their 64 KiB POST limit).
func appendBrowserDiagnosticSection(dst io.Writer, section []byte) error {
	f, ok := dst.(*os.File)
	if !ok {
		return errors.New("diagnostic destination is not seekable")
	}
	end, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	// Diagnostic producers emit a JSON object with bounded trailing whitespace.
	start := max(int64(0), end-4096)
	tail := make([]byte, end-start)
	if _, err = f.ReadAt(tail, start); err != nil {
		return err
	}
	i := len(tail) - 1
	for i >= 0 && (tail[i] == ' ' || tail[i] == '\n' || tail[i] == '\r' || tail[i] == '\t') {
		i--
	}
	if i < 0 || tail[i] != '}' {
		return errors.New("remote diagnostics did not end in a JSON object")
	}
	first := make([]byte, min(end, 4096))
	if _, err = f.ReadAt(first, 0); err != nil {
		return err
	}
	first = bytes.TrimSpace(first)
	if len(first) == 0 || first[0] != '{' {
		return errors.New("remote diagnostics is not a JSON object")
	}
	prefix := ",\n\"browserDiagnostics\":"
	if start == 0 && bytes.Equal(bytes.TrimSpace(tail[:i]), []byte("{")) {
		prefix = "\n\"browserDiagnostics\":"
	}
	if _, err = f.Seek(start+int64(i), io.SeekStart); err != nil {
		return err
	}
	if _, err = f.Write(append(append([]byte(prefix), section...), []byte("\n}\n")...)); err != nil {
		return err
	}
	position, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	return f.Truncate(position)
}
