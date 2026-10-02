package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"reasonix/internal/browser"
)

// worldName labels the isolated world in DevTools; page script can neither
// enumerate nor reach it.
const worldName = "reasonix-browser"

// snapshotBudget caps the nodes one snapshot may emit so a large document
// cannot flood the model's context.
const snapshotBudget = 2000

// pageIdentity is fixed for a tab's whole life: which target it is, which
// session may drive it, and which partition it belongs to.
type pageIdentity struct {
	id        string
	target    string
	session   string
	context   string
	owner     string
	temporary bool
}

// pageDocument is what one document version owns. Navigation, page
// replacement, and a take-over retire all of it together, which is why it is
// one value and not five independent flags: no combination of a live token
// with a dead isolated world can exist.
type pageDocument struct {
	frame     string
	world     int64
	token     string
	lastSeq   int64
	takenOver bool
}

// page is one agent-owned tab. Its mutex guards the document state and the
// per-tab records that outlive a single document.
type page struct {
	pageIdentity
	detach []func()

	mu        sync.Mutex
	doc       pageDocument
	loading   bool
	dead      bool
	url       string
	title     string
	downloads []string
}

// retire drops everything bound to the document that just went away: the
// isolated world holding the refs, the token those refs were promised under,
// and the take-over counter the new world starts over from. The frame outlives
// the document that was loaded in it.
func (p *page) retire(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.doc = pageDocument{frame: p.doc.frame}
	if url != "" {
		p.url = url
	}
}

func (p *page) tab() browser.Tab {
	p.mu.Lock()
	defer p.mu.Unlock()
	return browser.Tab{ID: p.id, URL: p.url, Title: p.title, Loading: p.loading, Temporary: p.temporary}
}

func (p *page) setLoading(loading bool) {
	p.mu.Lock()
	p.loading = loading
	p.mu.Unlock()
}

func (p *page) markDead() {
	p.mu.Lock()
	p.dead = true
	p.doc.world = 0
	p.doc.token = ""
	p.mu.Unlock()
}

func (p *page) isDead() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

// attach opens a flat DevTools session on target and wires the events that
// retire document state. The returned page is not yet bootstrapped: the
// isolated world is created lazily by the first snapshot.
func (e *Executor) attach(ctx context.Context, id, target, contextID, owner string, temporary bool) (*page, error) {
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := e.conn.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true}, &out); err != nil {
		return nil, fmt.Errorf("attach to tab: %w", err)
	}
	p := &page{pageIdentity: pageIdentity{
		id: id, target: target, session: out.SessionID, context: contextID, owner: owner, temporary: temporary,
	}}
	if err := e.conn.call(ctx, p.session, "Page.enable", nil, nil); err != nil {
		return nil, fmt.Errorf("enable page events: %w", err)
	}
	e.subscribe(p)
	e.refreshFrame(ctx, p)
	return p, nil
}

// subscribe binds the page's document lifetime to Chrome's frame events.
func (e *Executor) subscribe(p *page) {
	p.detach = append(p.detach,
		e.conn.on(p.session, "Page.frameNavigated", func(params json.RawMessage) {
			var ev struct {
				Frame struct {
					ID       string `json:"id"`
					ParentID string `json:"parentId"`
					URL      string `json:"url"`
				} `json:"frame"`
			}
			if err := json.Unmarshal(params, &ev); err != nil || ev.Frame.ParentID != "" {
				return
			}
			p.mu.Lock()
			p.doc.frame = ev.Frame.ID
			p.mu.Unlock()
			p.retire(ev.Frame.URL)
		}),
		e.conn.on(p.session, "Page.frameStartedLoading", func(json.RawMessage) { p.setLoading(true) }),
		e.conn.on(p.session, "Page.frameStoppedLoading", func(json.RawMessage) { p.setLoading(false) }),
		e.conn.on(p.session, "Runtime.executionContextsCleared", func(json.RawMessage) { p.retire("") }),
		e.conn.on(p.session, "Inspector.targetCrashed", func(json.RawMessage) { p.markDead() }),
	)
}

func (p *page) unsubscribe() {
	for _, off := range p.detach {
		off()
	}
	p.detach = nil
}

// refreshFrame records the main frame and the tab's current address.
func (e *Executor) refreshFrame(ctx context.Context, p *page) {
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID    string `json:"id"`
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := e.conn.call(ctx, p.session, "Page.getFrameTree", nil, &tree); err != nil {
		return
	}
	p.mu.Lock()
	p.doc.frame = tree.FrameTree.Frame.ID
	if p.url == "" {
		p.url = tree.FrameTree.Frame.URL
	}
	p.mu.Unlock()
}

// evalResult is the shape of a Runtime.evaluate or Runtime.callFunctionOn
// reply that this package cares about.
type evalResult struct {
	Result struct {
		Type     string          `json:"type"`
		Subtype  string          `json:"subtype"`
		Value    json.RawMessage `json:"value"`
		ObjectID string          `json:"objectId"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception *struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

func (r evalResult) exception() error {
	if r.ExceptionDetails == nil {
		return nil
	}
	if r.ExceptionDetails.Exception != nil && r.ExceptionDetails.Exception.Description != "" {
		return fmt.Errorf("page script failed: %s", firstLine(r.ExceptionDetails.Exception.Description))
	}
	return fmt.Errorf("page script failed: %s", r.ExceptionDetails.Text)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// ensureWorld returns the page's isolated world, creating and bootstrapping it
// when the previous document took the old one with it.
func (e *Executor) ensureWorld(ctx context.Context, p *page) (int64, error) {
	p.mu.Lock()
	world, frame := p.doc.world, p.doc.frame
	p.mu.Unlock()
	if world != 0 {
		return world, nil
	}
	if frame == "" {
		e.refreshFrame(ctx, p)
		p.mu.Lock()
		frame = p.doc.frame
		p.mu.Unlock()
	}
	if frame == "" {
		return 0, fmt.Errorf("tab %s has no main frame", p.id)
	}
	var out struct {
		ExecutionContextID int64 `json:"executionContextId"`
	}
	if err := e.conn.call(ctx, p.session, "Page.createIsolatedWorld", map[string]any{"frameId": frame, "worldName": worldName}, &out); err != nil {
		return 0, fmt.Errorf("create isolated world: %w", err)
	}
	var boot evalResult
	if err := e.conn.call(ctx, p.session, "Runtime.evaluate", evaluateParams(bootstrapJS, out.ExecutionContextID, true), &boot); err != nil {
		return 0, fmt.Errorf("install page helper: %w", err)
	}
	if err := boot.exception(); err != nil {
		return 0, err
	}
	p.mu.Lock()
	p.doc.world = out.ExecutionContextID
	p.doc.lastSeq = 0
	p.mu.Unlock()
	return out.ExecutionContextID, nil
}

func evaluateParams(expression string, contextID int64, byValue bool) map[string]any {
	return map[string]any{
		"expression":    expression,
		"contextId":     contextID,
		"returnByValue": byValue,
		"awaitPromise":  true,
		"userGesture":   true,
	}
}

// eval runs one expression in the page's isolated world and decodes its value.
// A world that died between the lookup and the call is rebuilt once, which is
// the ordinary race with a page navigating itself.
func (e *Executor) eval(ctx context.Context, p *page, expression string, out any) error {
	for attempt := range 2 {
		world, err := e.ensureWorld(ctx, p)
		if err != nil {
			return err
		}
		var res evalResult
		err = e.conn.call(ctx, p.session, "Runtime.evaluate", evaluateParams(expression, world, out != nil), &res)
		if err != nil {
			if attempt == 0 && staleContext(err) {
				p.retire("")
				continue
			}
			return err
		}
		if err := res.exception(); err != nil {
			return err
		}
		if out == nil {
			return nil
		}
		if len(res.Result.Value) == 0 {
			return fmt.Errorf("page helper returned no value")
		}
		return json.Unmarshal(res.Result.Value, out)
	}
	return fmt.Errorf("tab %s replaced its document while the call was in flight", p.id)
}

// evalHandle runs one expression and keeps the remote object alive so a DOM
// command can address the node it returned.
func (e *Executor) evalHandle(ctx context.Context, p *page, expression string) (string, error) {
	world, err := e.ensureWorld(ctx, p)
	if err != nil {
		return "", err
	}
	var res evalResult
	if err := e.conn.call(ctx, p.session, "Runtime.evaluate", evaluateParams(expression, world, false), &res); err != nil {
		return "", err
	}
	if err := res.exception(); err != nil {
		return "", err
	}
	if res.Result.Subtype == "null" || res.Result.ObjectID == "" {
		return "", browser.ErrStaleReference
	}
	return res.Result.ObjectID, nil
}

func (e *Executor) releaseHandle(ctx context.Context, p *page, objectID string) {
	if objectID == "" {
		return
	}
	_ = e.conn.call(ctx, p.session, "Runtime.releaseObject", map[string]any{"objectId": objectID}, nil)
}

func staleContext(err error) bool {
	var pe *protocolError
	if !errors.As(err, &pe) {
		return false
	}
	msg := strings.ToLower(pe.Message)
	return strings.Contains(msg, "cannot find context") || strings.Contains(msg, "execution context was destroyed")
}

// pageState is the isolated world's view of the document between calls.
type pageState struct {
	UserSeq int64  `json:"userSeq"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Ready   string `json:"ready"`
}

// observe reads the document's state and decides whether a human touched the
// page since the last snapshot. The take-over flag is sticky: only a fresh
// snapshot clears it, matching the shell's rule that the agent resumes after
// re-reading the page.
func (e *Executor) observe(ctx context.Context, p *page) (pageState, error) {
	var st pageState
	if err := e.eval(ctx, p, "__rx.state()", &st); err != nil {
		return pageState{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.url, p.title = st.URL, st.Title
	if st.UserSeq != p.doc.lastSeq {
		p.doc.lastSeq = st.UserSeq
		p.doc.takenOver = true
	}
	return st, nil
}

// markAgentInput opens the window in which trusted input is the executor's own
// rather than the user's.
func (e *Executor) markAgentInput(ctx context.Context, p *page, windowMillis int) error {
	var seq int64
	if err := e.eval(ctx, p, "__rx.window("+strconv.Itoa(windowMillis)+")", &seq); err != nil {
		return err
	}
	p.mu.Lock()
	p.doc.lastSeq = seq
	p.mu.Unlock()
	return nil
}
