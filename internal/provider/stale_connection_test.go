package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"reasonix/internal/netclient"
)

// deadUpstreamTunnel forwards bytes to a TLS backend. Connections accepted up
// to killThrough are dropped the moment the client writes on them, the way a
// local proxy whose upstream half died while idle only finds out on the next write.
type deadUpstreamTunnel struct {
	ln          net.Listener
	backend     string
	accepted    atomic.Int32
	killThrough atomic.Int32
}

// killExisting marks every connection accepted so far as dead upstream; new
// connections still reach the backend.
func (p *deadUpstreamTunnel) killExisting() { p.killThrough.Store(p.accepted.Load()) }

func (p *deadUpstreamTunnel) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.pipe(c, p.accepted.Add(1))
	}
}

func (p *deadUpstreamTunnel) pipe(c net.Conn, index int32) {
	b, err := net.Dial("tcp", p.backend)
	if err != nil {
		c.Close()
		return
	}
	go func() { _, _ = io.Copy(c, b); c.Close() }()
	buf := make([]byte, 32<<10)
	for {
		n, err := c.Read(buf)
		if n > 0 && index <= p.killThrough.Load() {
			c.Close()
			b.Close()
			return
		}
		if n > 0 {
			if _, werr := b.Write(buf[:n]); werr != nil {
				c.Close()
				return
			}
		}
		if err != nil {
			b.Close()
			return
		}
	}
}

type staleConnFixture struct {
	tunnel *deadUpstreamTunnel
	client *http.Client
	url    string
	hits   *atomic.Int32
}

func newStaleConnFixture(t *testing.T, http2 bool) *staleConnFixture {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		hits.Add(1)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	srv.EnableHTTP2 = http2
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	tunnel := &deadUpstreamTunnel{ln: ln, backend: srv.Listener.Addr().String()}
	go tunnel.serve()
	tr, err := netclient.NewTransport(netclient.ProxySpec{Mode: netclient.ModeOff}, netclient.TransportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	t.Cleanup(tr.CloseIdleConnections)
	return &staleConnFixture{tunnel: tunnel, client: &http.Client{Transport: tr}, url: "https://" + ln.Addr().String() + "/chat/completions", hits: &hits}
}

func (f *staleConnFixture) send(ctx context.Context) (*http.Response, error) {
	return SendWithRetry(ctx, f.client, SendOptions{Provider: "deepseek", Protocol: "openai"}, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, f.url, strings.NewReader(`{"stream":true}`))
	})
}

func (f *staleConnFixture) warm(t *testing.T) {
	t.Helper()
	resp, err := f.send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// #11254: after an idle pause the pooled connection is dead, and the next turn
// failed with "unexpected EOF" although the request never reached the provider.
func TestSendWithRetryResendsOnceWhenIdlePooledConnectionIsDead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		http2 bool
	}{{"http2", true}, {"http1", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStaleConnFixture(t, tc.http2)
			f.warm(t)
			f.tunnel.killExisting()
			ctx := WithRequestAttemptCounter(context.Background())
			resp, err := f.send(ctx)
			if err != nil {
				t.Fatalf("send on dead pooled connection: %v", err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if got := f.hits.Load(); got != 2 {
				t.Fatalf("provider saw %d requests, want 2 (warm-up + one delivery)", got)
			}
			if got := RequestAttemptCount(ctx); got != 2 {
				t.Fatalf("attempts = %d, want the failed write and the resend counted", got)
			}
		})
	}
}

// A connection that was never pooled carries no staleness: its failure is the
// network's, and the user decides whether to send again.
func TestSendWithRetryDoesNotResendOnFreshConnection(t *testing.T) {
	f := newStaleConnFixture(t, true)
	f.tunnel.killThrough.Store(1 << 30)
	ctx := WithRequestAttemptCounter(context.Background())
	_, err := f.send(ctx)
	if err == nil {
		t.Fatal("fresh connection failure was absorbed")
	}
	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T, want *RequestFailure", err)
	}
	if got := RequestAttemptCount(ctx); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestStaleIdleConnectionNeedsSilenceAndIdleReuse(t *testing.T) {
	cases := []struct {
		name      string
		idleReuse bool
		responded bool
		err       error
		want      bool
	}{
		{"idle reuse, silent, EOF", true, false, io.ErrUnexpectedEOF, true},
		{"not reused", false, false, io.ErrUnexpectedEOF, false},
		{"response bytes arrived", true, true, io.ErrUnexpectedEOF, false},
		{"not a closed connection", true, false, errors.New("tls: bad certificate"), false},
		{"deadline", true, false, context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &connectionProbe{}
			p.idleReuse.Store(tc.idleReuse)
			p.responded.Store(tc.responded)
			if got := p.staleIdleConnection(context.Background(), tc.err); got != tc.want {
				t.Fatalf("staleIdleConnection = %v, want %v", got, tc.want)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	p := &connectionProbe{}
	p.idleReuse.Store(true)
	if p.staleIdleConnection(canceled, io.ErrUnexpectedEOF) {
		t.Fatal("a canceled turn must not be resent")
	}
}
