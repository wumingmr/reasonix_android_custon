package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"reasonix/internal/control"
	"reasonix/internal/servecontract"
	"reasonix/internal/session"
	"reasonix/internal/sessionexport"
)

type SessionExportHandle struct {
	ExportID string                 `json:"exportId"`
	Snapshot session.ExportSnapshot `json:"snapshot"`
	Format   string                 `json:"format"`
}
type SessionExportChunk struct {
	Data       string `json:"data"`
	NextOffset int64  `json:"nextOffset"`
	Done       bool   `json:"done"`
}
type SessionExportPage struct {
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
	Data   string `json:"data"`
	Done   bool   `json:"done"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
type SessionExportResult struct {
	Paths   []string `json:"paths"`
	Records int      `json:"records"`
	Pages   int      `json:"pages"`
}
type sessionExportJob struct {
	mu             sync.Mutex
	handle         SessionExportHandle
	ctx            context.Context
	cancel         context.CancelFunc
	dir, path      string
	workspaceRoot  string
	sourceHostID   string
	query          *session.Query
	controller     *control.Controller
	client         *http.Client
	base, route    string
	observation    json.RawMessage
	browserScope   string // Fixed with the export source, before the save dialog.
	prepared       bool
	records, pages int
	pageOffset     int64
}

func (a *App) exportJob(id string) (*sessionExportJob, error) {
	a.sessionExportMu.Lock()
	defer a.sessionExportMu.Unlock()
	job := a.sessionExports[id]
	if job == nil {
		return nil, errors.New("session export is no longer available")
	}
	return job, nil
}

// BeginSessionExportForTarget captures the source before the native dialog. A
// tab is only a compatibility resolver; no later operation consults that tab.
func (a *App) BeginSessionExportForTarget(selector SessionSelector, tabID, format, title, observation string) (SessionExportHandle, error) {
	switch format {
	case "markdown", "json", "pdf", "image", "clipboard", "diagnostic":
	default:
		return SessionExportHandle{}, errors.New("unsupported export format")
	}
	ctx, cancel := context.WithCancel(a.bootContext())
	job := &sessionExportJob{ctx: ctx, cancel: cancel, observation: json.RawMessage(observation)}
	success := false
	defer func() {
		if !success {
			cancel()
			if job.dir != "" {
				_ = os.RemoveAll(job.dir)
			}
		}
	}()
	if len(observation) > 64<<10 || (observation != "" && !json.Valid(job.observation)) {
		return SessionExportHandle{}, errors.New("invalid export observation")
	}
	if err := a.captureSessionExportSource(job, selector, tabID, format); err != nil {
		return SessionExportHandle{}, err
	}
	if title != "" {
		job.handle.Snapshot.Title = title
	}
	job.handle.ExportID = "export-" + newTabID()
	job.handle.Format = format
	var err error
	if format != "clipboard" {
		extension, mime := ".md", "text/markdown"
		switch format {
		case "json", "diagnostic":
			extension, mime = ".json", "application/json"
		case "pdf":
			extension, mime = ".pdf", "application/pdf"
		case "image":
			extension, mime = ".png", "image/png"
		}
		base := job.handle.Snapshot.Title
		if format == "diagnostic" {
			base += "-session-diagnostics"
		}
		job.path, err = a.nativeHost().SaveFileDialog(ctx, nativeDialogOptions{Title: "Export session", DefaultFilename: safeExportFilename(base + extension), CanCreateDirectories: true, Filters: exportFileFilters(mime, extension)})
		if err != nil {
			return SessionExportHandle{}, err
		}
		if job.path == "" {
			return SessionExportHandle{}, nil
		}
		if filepath.Ext(job.path) == "" {
			job.path += extension
		}
	}
	job.dir, err = os.MkdirTemp("", "reasonix-desktop-export-")
	if err != nil {
		return SessionExportHandle{}, err
	}
	a.sessionExportMu.Lock()
	if a.sessionExports == nil {
		a.sessionExports = map[string]*sessionExportJob{}
	}
	a.sessionExports[job.handle.ExportID] = job
	a.sessionExportMu.Unlock()
	success = true
	a.exportProgress(job, "preparing")
	return job.handle, nil
}

func (a *App) exportProgress(job *sessionExportJob, phase string) {
	a.emitRuntimeEvent("session_export_progress", map[string]any{"exportId": job.handle.ExportID, "title": job.handle.Snapshot.Title, "phase": phase, "records": job.records, "pages": job.pages})
}
func (a *App) prepareSessionExport(job *sessionExportJob) error {
	if err := job.ctx.Err(); err != nil {
		return err
	}
	if job.prepared {
		return nil
	}
	a.exportProgress(job, "reading")
	if job.client != nil {
		format := job.handle.Format
		if format == "clipboard" {
			format = "markdown"
		}
		if format == "pdf" || format == "image" {
			format = "blocks"
		}
		request, _ := json.Marshal(map[string]any{"snapshot": job.handle.Snapshot, "format": format})
		resp, err := serveDoForSession(job.ctx, job.client, http.MethodPost, sessionExportURL(job.base, "/session-export/document", job.handle.Snapshot.Ref.SessionID, false), request, job.route)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New("remote export failed or source changed; upgrade the remote service if the session was switched or taken over")
		}
		job.records, err = strconv.Atoi(resp.Header.Get("X-Reasonix-Export-Records"))
		if err != nil || job.records < 0 {
			return errors.New("remote export record count is invalid")
		}
		file, err := os.OpenFile(filepath.Join(job.dir, format), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, resp.Body)
		closeErr := file.Close()
		if err = errors.Join(copyErr, closeErr); err != nil {
			return err
		}
	} else {
		doc, err := sessionexport.Build(job.ctx, job.query, job.handle.Snapshot, job.dir, func(count int) {
			job.records = count
			if count%100 == 0 {
				a.exportProgress(job, "reading")
			}
		})
		if err != nil {
			return err
		}
		job.records = doc.Records
	}
	job.prepared = true
	a.exportProgress(job, "rendering")
	return nil
}

func (a *App) ReadSessionExportChunk(id string, offset int64) (SessionExportChunk, error) {
	job, err := a.exportJob(id)
	if err != nil {
		return SessionExportChunk{}, err
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if offset < 0 {
		return SessionExportChunk{}, errors.New("invalid export offset")
	}
	if err = a.prepareSessionExport(job); err != nil {
		return SessionExportChunk{}, err
	}
	format := "blocks"
	if job.handle.Format == "clipboard" {
		format = "markdown"
	}
	file, err := os.Open(filepath.Join(job.dir, format))
	if err != nil {
		return SessionExportChunk{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return SessionExportChunk{}, err
	}
	if offset > info.Size() {
		return SessionExportChunk{}, errors.New("invalid export offset")
	}
	bytes := make([]byte, min(int64(1<<20), info.Size()-offset))
	n, err := file.ReadAt(bytes, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return SessionExportChunk{}, err
	}
	return SessionExportChunk{Data: base64.StdEncoding.EncodeToString(bytes[:n]), NextOffset: offset + int64(n), Done: offset+int64(n) == info.Size()}, nil
}

func (a *App) AppendSessionExportPage(id string, page SessionExportPage) error {
	job, err := a.exportJob(id)
	if err != nil {
		return err
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if err = job.ctx.Err(); err != nil {
		return err
	}
	if job.handle.Format != "pdf" && job.handle.Format != "image" {
		return errors.New("export does not accept pages")
	}
	if page.Index != job.pages || page.Offset != job.pageOffset || len(page.Data) > 2<<20 {
		return errors.New("export page order or size is invalid")
	}
	data, err := base64.StdEncoding.DecodeString(page.Data)
	if len(data) > 1<<20 {
		return errors.New("export page chunk exceeds one MiB")
	}
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(job.dir, fmt.Sprintf("page-%06d", page.Index)), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, writeErr := file.WriteAt(data, page.Offset)
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	job.pageOffset += int64(n)
	if page.Done {
		if page.Width <= 0 || page.Height <= 0 || page.Width > 8192 || page.Height > 8192 {
			return errors.New("invalid export page dimensions")
		}
		source, openErr := os.Open(filepath.Join(job.dir, fmt.Sprintf("page-%06d", page.Index)))
		if openErr != nil {
			return openErr
		}
		config, encoding, decodeErr := image.DecodeConfig(source)
		expected := "png"
		if job.handle.Format == "pdf" {
			expected = "jpeg"
		}
		if decodeErr != nil || encoding != expected || config.Width != page.Width || config.Height != page.Height {
			source.Close()
			return errors.New("export page encoding or dimensions do not match")
		}
		if _, err := source.Seek(0, io.SeekStart); err != nil {
			source.Close()
			return err
		}
		_, _, decodeErr = image.Decode(source)
		source.Close()
		if decodeErr != nil {
			return fmt.Errorf("incomplete export page: %w", decodeErr)
		}

		meta, _ := json.Marshal(pageDimensions{Width: page.Width, Height: page.Height})
		if err = os.WriteFile(filepath.Join(job.dir, fmt.Sprintf("page-%06d.json", page.Index)), meta, 0600); err != nil {
			return err
		}
		job.pages++
		job.pageOffset = 0
		a.exportProgress(job, "rendering")
	}
	return nil
}

func (a *App) FinishSessionExport(id string) (SessionExportResult, error) {
	result := SessionExportResult{Paths: []string{}}
	job, err := a.exportJob(id)
	if err != nil {
		return result, err
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if err = job.ctx.Err(); err != nil {
		return result, err
	}
	if err = a.validateSessionExportSource(job); err != nil {
		return result, err
	}

	if job.pageOffset != 0 {
		return result, errors.New("export has an incomplete page")
	}
	if job.handle.Format == "diagnostic" {
		err = a.writeSessionDiagnosticExport(job)
	} else {
		if err = a.prepareSessionExport(job); err != nil {
			return result, err
		}
		if err = a.validateSessionExportSource(job); err != nil {
			return result, err
		}
		a.exportProgress(job, "saving")
		switch job.handle.Format {
		case "markdown", "json":
			err = writeStreamingExport(job.path, func(dst io.Writer) error {
				src, err := os.Open(filepath.Join(job.dir, job.handle.Format))
				if err != nil {
					return err
				}
				defer src.Close()
				_, err = copyExportContext(job.ctx, dst, src)
				return err
			})
		case "pdf":
			err = writeStreamingExport(job.path, func(dst io.Writer) error {
				return writeExportPDF(job.ctx, dst, job.dir, job.pages, job.handle.Snapshot.Title)
			})
		case "image":
			result.Paths, err = publishExportImages(job.ctx, job.dir, job.path, job.pages)
		case "clipboard":
		}
	}
	if err != nil {
		return result, err
	}
	if job.path != "" && len(result.Paths) == 0 {
		result.Paths = append(result.Paths, job.path)
	}
	result.Records = job.records
	result.Pages = job.pages
	a.exportProgress(job, "complete")
	a.sessionExportMu.Lock()
	delete(a.sessionExports, id)
	a.sessionExportMu.Unlock()
	job.cancel()
	_ = os.RemoveAll(job.dir)
	return result, nil
}

func (a *App) CancelSessionExport(id string) error {
	a.sessionExportMu.Lock()
	job := a.sessionExports[id]
	delete(a.sessionExports, id)
	a.sessionExportMu.Unlock()
	if job == nil {
		return nil
	}
	job.cancel()
	job.mu.Lock()
	defer job.mu.Unlock()
	_ = os.RemoveAll(job.dir)
	a.exportProgress(job, "cancelled")
	return nil
}
func (a *App) cancelSessionExports() {
	a.sessionExportMu.Lock()
	jobs := a.sessionExports
	a.sessionExports = nil
	a.sessionExportMu.Unlock()
	for _, job := range jobs {
		job.cancel()
		go func(j *sessionExportJob) { j.mu.Lock(); defer j.mu.Unlock(); _ = os.RemoveAll(j.dir) }(job)
	}
}

type exportContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r exportContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func copyExportContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, exportContextReader{ctx, src})
}

func (a *App) validateSessionExportSource(job *sessionExportJob) error {
	if job.client == nil {
		return job.query.ValidateExportSource(job.handle.Snapshot)
	}
	body, _ := json.Marshal(job.handle.Snapshot)
	response, err := serveDoForSession(job.ctx, job.client, http.MethodPost, sessionExportURL(job.base, "/session-export/validate", job.handle.Snapshot.Ref.SessionID, false), body, job.route)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("export source changed or is unavailable; upgrade the remote service if the session was switched or taken over")
	}
	return nil
}

func (a *App) captureSessionExportSource(job *sessionExportJob, selector SessionSelector, tabID, format string) error {
	ctx := job.ctx
	if a.isRemoteTab(tabID) {
		a.remoteTabMu.Lock()
		tab := a.remoteTabs[tabID]
		if tab == nil || !tab.capabilities[servecontract.SessionExportV1] {
			a.remoteTabMu.Unlock()
			return errors.New("remote service does not support session-export-v1; upgrade it to export the complete session")
		}
		if !remoteExportSelectorMatches(selector, tab) {
			a.remoteTabMu.Unlock()
			return errors.New("export target changed")
		}
		job.sourceHostID, job.workspaceRoot = tab.ref.HostID, tab.ref.Workspace
		job.client, job.base, job.route = tab.client, tab.base, tab.routing.currentPath
		a.remoteTabMu.Unlock()
		if job.client == nil || job.route == "" {
			return errors.New("remote session is unavailable")
		}
		sessionID, ok := strings.CutPrefix(job.route, remoteSessionIDRoutePrefix)
		if !ok || sessionID == "" {
			return errors.New("remote session has no canonical identity")
		}
		resp, err := serveDoForSession(ctx, job.client, http.MethodGet, sessionExportURL(job.base, "/session-export/snapshot", sessionID, format == "diagnostic"), nil, job.route)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New("unable to capture remote export snapshot")
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&job.handle.Snapshot); err != nil {
			return err
		}
		if selector.Ref != nil && selector.Ref.SessionID != job.handle.Snapshot.Ref.SessionID {
			return errors.New("export target changed")
		}
		job.browserScope = browserDiagnosticScope(job.sourceHostID, job.handle.Snapshot.Ref.SessionID)
	} else {
		if selector.Ref == nil && selector.Source == nil && selector.SessionPath == "" && selector.TopicID == "" {
			a.mu.RLock()
			tab := a.tabByIDLocked(tabID)
			if tab != nil {
				selector.SessionPath = tab.currentSessionPath()
				if tab.SessionID != "" {
					selector.Ref = &session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}
				}
			}
			a.mu.RUnlock()
		}
		target, err := a.resolveSessionTargetWithArchived(selector, true)
		if err != nil {
			return err
		}
		if target.SessionRef.SessionID == "" {
			return newSessionOperationError("unsupported", "This historical format cannot guarantee a complete export.")
		}
		job.query = a.desktopSessionService("").Query()
		job.controller = target.Controller
		job.browserScope = browserDiagnosticScope(localDesktopHostID, target.SessionRef.SessionID)
		job.workspaceRoot = target.WorkspaceRoot
		if format == "diagnostic" {
			job.handle.Snapshot, err = job.query.CaptureDiagnosticSnapshot(ctx, target.SessionRef)
			if err != nil {
				return err
			}
		} else {
			job.handle.Snapshot, err = job.query.CaptureExportSnapshot(ctx, target.SessionRef)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func sessionExportURL(base, path, sessionID string, diagnostic bool) string {
	endpoint, err := url.Parse(serveURL(base, path))
	if err != nil {
		return serveURL(base, path)
	}
	query := endpoint.Query()
	if sessionID != "" {
		query.Set("sessionId", sessionID)
	}
	if diagnostic {
		query.Set("diagnostic", "1")
	}
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

// Called under remoteTabMu before any remote read or save dialog.
func remoteExportSelectorMatches(selector SessionSelector, tab *remoteTab) bool {
	if selector.Ref != nil {
		return selector.Ref.HostID == tab.ref.HostID && remoteSessionIDRoutePrefix+selector.Ref.SessionID == tab.routing.currentPath
	}
	if selector.Source != nil {
		return selector.Source.HostID == tab.ref.HostID && (selector.Source.Path == tab.session.path || selector.Source.Path == tab.routing.currentPath)
	}
	return selector.SessionPath == "" || selector.SessionPath == tab.session.path || selector.SessionPath == tab.routing.currentPath
}
