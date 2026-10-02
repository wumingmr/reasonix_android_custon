package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Screenshots and accessibility trees travel inline on this socket, so the
	// read limit sits well above any control frame.
	maxFrameBytes = 96 << 20
	writeTimeout  = 15 * time.Second
)

// errConnClosed reports that the DevTools socket is gone; callers translate it
// into an unknown outcome for writes and a plain failure for reads.
var errConnClosed = errors.New("cdp: devtools connection closed")

// message is one DevTools frame in either direction. Commands carry ID and
// Method, replies carry ID with Result or Error, and events carry Method only.
type message struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *protocolError  `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// protocolError is Chrome's refusal of one command.
type protocolError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *protocolError) Error() string {
	if e.Data == "" {
		return fmt.Sprintf("cdp: %s (%d)", e.Message, e.Code)
	}
	return fmt.Sprintf("cdp: %s: %s (%d)", e.Message, e.Data, e.Code)
}

// handler observes events of one method on one session; a true return
// unsubscribes it. Handlers run on the read loop and must never call conn.
type handler struct {
	session string
	method  string
	fn      func(params json.RawMessage) bool
}

// conn multiplexes commands and events over one DevTools WebSocket. Flat
// sessions put every target on this socket, so IDs are unique across sessions
// and only the sessionId field says which target answered.
type conn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan message
	handlers map[int]handler
	nextHnd  int
	err      error

	done chan struct{}
}

// dialConn opens the DevTools socket at wsURL and starts its read loop.
func dialConn(ctx context.Context, wsURL string) (*conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 20 * time.Second, ReadBufferSize: 64 << 10, WriteBufferSize: 64 << 10}
	ws, resp, err := dialer.DialContext(ctx, wsURL, http.Header{})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("cdp: dial %s: %w (status %d)", wsURL, err, resp.StatusCode)
		}
		return nil, fmt.Errorf("cdp: dial %s: %w", wsURL, err)
	}
	ws.SetReadLimit(maxFrameBytes)
	c := &conn{ws: ws, pending: map[int64]chan message{}, handlers: map[int]handler{}, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *conn) readLoop() {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.shutdown(err)
			return
		}
		var msg message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID != 0 {
			c.deliver(msg)
			continue
		}
		c.dispatch(msg)
	}
}

func (c *conn) deliver(msg message) {
	c.mu.Lock()
	ch, ok := c.pending[msg.ID]
	delete(c.pending, msg.ID)
	c.mu.Unlock()
	if ok {
		ch <- msg
	}
}

// dispatch fans one event out to its handlers. The snapshot is taken under the
// lock and the handlers run without it, so a handler may unsubscribe itself.
func (c *conn) dispatch(msg message) {
	c.mu.Lock()
	matched := make([]int, 0, 4)
	for id, h := range c.handlers {
		if h.method == msg.Method && (h.session == "" || h.session == msg.SessionID) {
			matched = append(matched, id)
		}
	}
	fns := make(map[int]func(json.RawMessage) bool, len(matched))
	for _, id := range matched {
		fns[id] = c.handlers[id].fn
	}
	c.mu.Unlock()
	for id, fn := range fns {
		if fn(msg.Params) {
			c.removeHandler(id)
		}
	}
}

func (c *conn) shutdown(cause error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	if cause == nil {
		cause = errConnClosed
	}
	c.err = cause
	pending := c.pending
	c.pending = map[int64]chan message{}
	c.mu.Unlock()
	close(c.done)
	for _, ch := range pending {
		close(ch)
	}
	_ = c.ws.Close()
}

// close tears the socket down; in-flight callers observe errConnClosed.
func (c *conn) close() {
	c.shutdown(errConnClosed)
}

func (c *conn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *conn) addHandler(session, method string, fn func(json.RawMessage) bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextHnd++
	id := c.nextHnd
	c.handlers[id] = handler{session: session, method: method, fn: fn}
	return id
}

func (c *conn) removeHandler(id int) {
	c.mu.Lock()
	delete(c.handlers, id)
	c.mu.Unlock()
}

// on subscribes to every event of method on session ("" for any session) and
// returns the unsubscribe function.
func (c *conn) on(session, method string, fn func(json.RawMessage)) func() {
	id := c.addHandler(session, method, func(p json.RawMessage) bool {
		fn(p)
		return false
	})
	return func() { c.removeHandler(id) }
}

// once delivers the first matching event and unsubscribes. The returned
// cancel must run even when the event arrives, so callers defer it.
func (c *conn) once(session, method string) (<-chan json.RawMessage, func()) {
	ch := make(chan json.RawMessage, 1)
	id := c.addHandler(session, method, func(p json.RawMessage) bool {
		ch <- p
		return true
	})
	return ch, func() { c.removeHandler(id) }
}

// call sends one command and waits for its reply. A cancelled context or a
// dead socket returns without a verdict, which write paths translate into
// browser.ErrUnknownOutcome.
func (c *conn) call(ctx context.Context, session, method string, params, out any) error {
	body, err := encodeParams(params)
	if err != nil {
		return fmt.Errorf("cdp: encode %s: %w", method, err)
	}
	reply := make(chan message, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = reply
	c.mu.Unlock()

	if err := c.write(message{ID: id, Method: method, Params: body, SessionID: session}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.done:
		return c.failure()
	case msg, ok := <-reply:
		if !ok {
			return c.failure()
		}
		if msg.Error != nil {
			return msg.Error
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(msg.Result, out); err != nil {
			return fmt.Errorf("cdp: decode %s reply: %w", method, err)
		}
		return nil
	}
}

func (c *conn) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return errConnClosed
}

func (c *conn) write(msg message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("cdp: encode %s: %w", msg.Method, err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("cdp: %s: %w", msg.Method, err)
	}
	if err := c.ws.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("cdp: %s: %w", msg.Method, err)
	}
	return nil
}

func encodeParams(params any) (json.RawMessage, error) {
	if params == nil {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(params)
}
