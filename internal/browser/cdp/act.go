package cdp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"reasonix/internal/browser"
)

// agentInputWindow is how long after a marker trusted input still counts as
// the executor's own. It covers one dispatch, not a train of them.
const agentInputWindowMillis = 1500

// maxTypeRunes bounds one browser_type so a runaway argument cannot hold the
// socket for minutes: each rune costs two key events.
const maxTypeRunes = 10000

// errDispatched marks a failure that happened after the page already felt the
// action, which is the only honest reason to report an unknown outcome.
var errDispatched = errors.New("input reached the page")

// Snapshot re-reads the document, mints the token its refs are bound to, and
// clears a take-over: re-reading the page is exactly how the agent resumes.
func (e *Executor) Snapshot(ctx context.Context, req browser.SnapshotRequest) (browser.Snapshot, error) {
	p, err := e.lookup(ctx, req.TabID)
	if err != nil {
		return browser.Snapshot{}, err
	}
	selector, err := json.Marshal(nullableString(req.Selector))
	if err != nil {
		return browser.Snapshot{}, err
	}
	var out struct {
		Error     string `json:"error"`
		URL       string `json:"url"`
		Title     string `json:"title"`
		Tree      string `json:"tree"`
		Refs      int    `json:"refs"`
		UserSeq   int64  `json:"userSeq"`
		Truncated bool   `json:"truncated"`
	}
	expr := fmt.Sprintf("__rx.snapshot(%s, %d)", selector, snapshotBudget)
	if err := e.eval(ctx, p, expr, &out); err != nil {
		return browser.Snapshot{}, err
	}
	if out.Error != "" {
		return browser.Snapshot{}, fmt.Errorf("browser_snapshot: %s", out.Error)
	}
	token := mintToken()
	p.mu.Lock()
	p.doc.token, p.doc.lastSeq, p.doc.takenOver = token, out.UserSeq, false
	p.url, p.title = out.URL, out.Title
	p.mu.Unlock()

	tree := out.Tree
	if out.Truncated {
		tree += fmt.Sprintf("\n… snapshot stopped at %d nodes; pass selector to scope it to one subtree.", snapshotBudget)
	}
	return browser.Snapshot{DocumentToken: token, URL: out.URL, Title: out.Title, Tree: tree, Refs: out.Refs}, nil
}

// Act performs one reserved write after proving the model is acting on the
// document it last read.
func (e *Executor) Act(ctx context.Context, req browser.ActRequest) (browser.ActResult, error) {
	p, err := e.lookup(ctx, req.TabID)
	if err != nil {
		return browser.ActResult{}, err
	}
	if err := e.guard(ctx, p, req.DocumentToken); err != nil {
		return browser.ActResult{}, err
	}
	if err := e.reserve(req.OperationID, req.Action+" on "+req.TabID); err != nil {
		return browser.ActResult{}, err
	}
	executed, reason, err := e.perform(ctx, p, req)
	if err != nil {
		return e.failure(err)
	}
	p.mu.Lock()
	token := p.doc.token
	p.mu.Unlock()
	if !executed {
		return browser.ActResult{Reason: reason, Outcome: browser.OutcomeNotExecuted, DocumentToken: token}, nil
	}
	return browser.ActResult{Executed: true, Outcome: browser.OutcomeExecuted, DocumentToken: token}, nil
}

// failure decides what a failed write leaves behind. A refusal the executor
// recognises keeps its sentinel; a failure after input reached the page is the
// one case where the outcome is genuinely unknown; anything else never left
// this process, so the model may plan again with a fresh operationId.
func (e *Executor) failure(err error) (browser.ActResult, error) {
	switch {
	case errors.Is(err, browser.ErrStaleReference), errors.Is(err, browser.ErrTakenOver), errors.Is(err, browser.ErrNoGrant):
		return browser.ActResult{Outcome: browser.OutcomeNotExecuted}, err
	case errors.Is(err, errDispatched):
		return browser.ActResult{Outcome: browser.OutcomeUnknown}, fmt.Errorf("%w: %w", browser.ErrUnknownOutcome, err)
	}
	return browser.ActResult{Reason: err.Error(), Outcome: browser.OutcomeNotExecuted}, nil
}

// guard refuses a write that was planned against a document the tab has left
// or that a human has touched since the snapshot.
func (e *Executor) guard(ctx context.Context, p *page, token string) error {
	p.mu.Lock()
	current, takenOver := p.doc.token, p.doc.takenOver
	p.mu.Unlock()
	switch {
	case takenOver:
		return browser.ErrTakenOver
	case current == "" || current != token:
		return browser.ErrStaleReference
	}
	if _, err := e.observe(ctx, p); err != nil {
		return err
	}
	p.mu.Lock()
	takenOver, current = p.doc.takenOver, p.doc.token
	p.mu.Unlock()
	switch {
	case takenOver:
		return browser.ErrTakenOver
	case current != token:
		return browser.ErrStaleReference
	}
	return nil
}

// perform dispatches one action. A nil error with executed false is a refusal
// the model can act on; a non-nil error means the outcome is not known.
func (e *Executor) perform(ctx context.Context, p *page, req browser.ActRequest) (bool, string, error) {
	switch req.Action {
	case browser.ActionClick:
		return e.click(ctx, p, req)
	case browser.ActionType:
		return e.typeText(ctx, p, req)
	case browser.ActionPress:
		return e.press(ctx, p, req)
	case browser.ActionScroll:
		return e.scroll(ctx, p, req)
	case browser.ActionSelect:
		return e.selectOptions(ctx, p, req)
	case browser.ActionUpload:
		return e.upload(ctx, p, req)
	}
	return false, "unsupported action " + req.Action, nil
}

// elementRect is the isolated world's answer for one ref: its viewport centre
// after scrolling it into view, or a hidden marker.
type elementRect struct {
	Hidden bool    `json:"hidden"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Tag    string  `json:"tag"`
	Type   string  `json:"type"`
}

// locate resolves a ref. A ref the document no longer holds is stale, which is
// a refusal the tools translate into "snapshot again".
func (e *Executor) locate(ctx context.Context, p *page, ref string) (*elementRect, error) {
	var rect *elementRect
	if err := e.eval(ctx, p, fmt.Sprintf("__rx.rect(%s)", quote(ref)), &rect); err != nil {
		return nil, err
	}
	if rect == nil {
		return nil, browser.ErrStaleReference
	}
	return rect, nil
}

func (e *Executor) click(ctx context.Context, p *page, req browser.ActRequest) (bool, string, error) {
	rect, err := e.locate(ctx, p, req.Ref)
	if err != nil {
		return false, "", err
	}
	if rect.Hidden {
		return false, "the element has no visible box on the page", nil
	}
	if err := e.markAgentInput(ctx, p, agentInputWindowMillis); err != nil {
		return false, "", err
	}
	base := map[string]any{"x": rect.X, "y": rect.Y, "button": "left", "clickCount": 1, "buttons": 1}
	steps := []map[string]any{
		merge(base, map[string]any{"type": "mouseMoved", "buttons": 0}),
		merge(base, map[string]any{"type": "mousePressed"}),
		merge(base, map[string]any{"type": "mouseReleased", "buttons": 0}),
	}
	return e.dispatchAll(ctx, p, "Input.dispatchMouseEvent", steps)
}

func (e *Executor) typeText(ctx context.Context, p *page, req browser.ActRequest) (bool, string, error) {
	runes := []rune(req.Text)
	if len(runes) > maxTypeRunes {
		return false, fmt.Sprintf("text is %d characters, over the %d-character limit for one call", len(runes), maxTypeRunes), nil
	}
	ok, reason, err := e.focusRef(ctx, p, req.Ref)
	if !ok || err != nil {
		return false, reason, err
	}
	if err := e.markAgentInput(ctx, p, agentInputWindowMillis+len(runes)*10); err != nil {
		return false, "", err
	}
	var events []map[string]any
	for _, r := range runes {
		events = append(events, charEvents(r)...)
	}
	if req.Submit {
		down, up, err := chordEvents("Enter")
		if err != nil {
			return false, "", err
		}
		events = append(events, down, up)
	}
	return e.dispatchAll(ctx, p, "Input.dispatchKeyEvent", events)
}

func (e *Executor) press(ctx context.Context, p *page, req browser.ActRequest) (bool, string, error) {
	if strings.TrimSpace(req.Ref) != "" {
		ok, reason, err := e.focusRef(ctx, p, req.Ref)
		if !ok || err != nil {
			return false, reason, err
		}
	}
	down, up, err := chordEvents(req.Keys)
	if err != nil {
		return false, err.Error(), nil
	}
	if err := e.markAgentInput(ctx, p, agentInputWindowMillis); err != nil {
		return false, "", err
	}
	return e.dispatchAll(ctx, p, "Input.dispatchKeyEvent", []map[string]any{down, up})
}

func (e *Executor) scroll(ctx context.Context, p *page, req browser.ActRequest) (bool, string, error) {
	x, y := 0.0, 0.0
	if strings.TrimSpace(req.Ref) != "" {
		rect, err := e.locate(ctx, p, req.Ref)
		if err != nil {
			return false, "", err
		}
		if rect.Hidden {
			return false, "the element has no visible box on the page", nil
		}
		x, y = rect.X, rect.Y
	} else {
		metrics, err := e.layout(ctx, p)
		if err != nil {
			return false, "", err
		}
		x, y = metrics.viewWidth/2, metrics.viewHeight/2
	}
	if err := e.markAgentInput(ctx, p, agentInputWindowMillis); err != nil {
		return false, "", err
	}
	event := map[string]any{"type": "mouseWheel", "x": x, "y": y, "deltaX": req.DeltaX, "deltaY": req.DeltaY}
	return e.dispatchAll(ctx, p, "Input.dispatchMouseEvent", []map[string]any{event})
}

// selectOptions drives a select element through the DOM: a native dropdown is
// rendered by the platform and cannot be steered with synthetic mouse events.
func (e *Executor) selectOptions(ctx context.Context, p *page, req browser.ActRequest) (bool, string, error) {
	options, err := json.Marshal(req.Options)
	if err != nil {
		return false, "", err
	}
	var out struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	expr := fmt.Sprintf("__rx.select(%s, %s)", quote(req.Ref), options)
	if err := e.eval(ctx, p, expr, &out); err != nil {
		return false, "", fmt.Errorf("%w: %w", errDispatched, err)
	}
	return out.OK, out.Reason, nil
}

func (e *Executor) upload(ctx context.Context, p *page, req browser.ActRequest) (bool, string, error) {
	files := make([]string, 0, len(req.Files))
	for _, candidate := range req.Files {
		resolved, refusal := e.uploads.resolve(candidate)
		if refusal != "" {
			return false, refusal, nil
		}
		files = append(files, resolved)
	}
	handle, err := e.evalHandle(ctx, p, fmt.Sprintf("__rx.element(%s)", quote(req.Ref)))
	if err != nil {
		return false, "", err
	}
	defer e.releaseHandle(ctx, p, handle)
	if err := e.conn.call(ctx, p.session, "DOM.enable", nil, nil); err != nil {
		return false, "", err
	}
	if err := e.conn.call(ctx, p.session, "DOM.setFileInputFiles", map[string]any{"objectId": handle, "files": files}, nil); err != nil {
		var pe *protocolError
		if errors.As(err, &pe) {
			return false, pe.Message, nil
		}
		return false, "", fmt.Errorf("%w: %w", errDispatched, err)
	}
	return true, "", nil
}

// focusRef puts the keyboard on one ref before typing into it.
func (e *Executor) focusRef(ctx context.Context, p *page, ref string) (bool, string, error) {
	if _, err := e.locate(ctx, p, ref); err != nil {
		return false, "", err
	}
	var out struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if err := e.eval(ctx, p, fmt.Sprintf("__rx.focus(%s)", quote(ref)), &out); err != nil {
		return false, "", err
	}
	return out.OK, out.Reason, nil
}

// dispatchAll sends a train of input events. Once the first one lands, a later
// failure leaves the page half-acted-on, which is an unknown outcome and never
// a refusal the model may retry.
func (e *Executor) dispatchAll(ctx context.Context, p *page, method string, events []map[string]any) (bool, string, error) {
	for i, event := range events {
		if err := e.conn.call(ctx, p.session, method, event, nil); err != nil {
			if i > 0 {
				return false, "", fmt.Errorf("%w: %w", errDispatched, err)
			}
			var pe *protocolError
			if errors.As(err, &pe) {
				return false, pe.Message, nil
			}
			return false, "", err
		}
	}
	return true, "", nil
}

func merge(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	maps.Copy(out, base)
	maps.Copy(out, over)
	return out
}

func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func nullableString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// mintToken returns an opaque document token. Opacity matters: a model must
// not be able to guess the token of a document it has not read.
func mintToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "d-" + hex.EncodeToString(raw[:4])
	}
	return "d-" + hex.EncodeToString(raw[:])
}
