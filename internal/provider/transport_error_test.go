package provider

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"
)

func TestTransportRecoveryRequiresNetworkCause(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"invalid URL", errors.New(`unsupported protocol scheme ""`), false},
		{"invalid header", errors.New(`net/http: invalid header field name "bad header"`), false},
		{"missing history file", &os.PathError{Op: "open", Path: "old.jsonl", Err: os.ErrNotExist}, false},
		{"storage permission", &os.PathError{Op: "open", Path: "old.jsonl", Err: syscall.EACCES}, false},
		{"storage full", &os.SyscallError{Syscall: "write", Err: syscall.ENOSPC}, false},
		{"invalid address", &net.AddrError{Err: "missing port", Addr: "provider"}, false},
		{"certificate", x509.UnknownAuthorityError{}, false},
		{"cancel", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"EOF", io.EOF, true},
		{"truncated body", io.ErrUnexpectedEOF, true},
		{"closed socket", net.ErrClosed, true},
		{"reset", syscall.ECONNRESET, true},
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"read failure", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("wsarecv: forcibly closed")}, true},
		{"DNS", &net.DNSError{Err: "temporary failure", Name: "provider.invalid", IsTemporary: true}, true},
		{"timeout", os.ErrDeadlineExceeded, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, err := range []error{tt.err, &url.Error{Op: "Post", URL: "https://provider.invalid", Err: tt.err}} {
				err = fmt.Errorf("provider request: %w", err)
				if got := IsConnReset(err); got != tt.want {
					t.Errorf("IsConnReset(%v) = %v, want %v", err, got, tt.want)
				}
				if got := ClassifyRecovery(err); got.Retryable != tt.want {
					t.Errorf("ClassifyRecovery(%v) = %+v, want retryable %v", err, got, tt.want)
				}
			}
		})
	}
}

func TestSendWithRetryRejectsPermanentTransportErrors(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed=%v", managed), func(t *testing.T) {
			cause := errors.New("invalid request configuration")
			calls, retries := 0, 0
			client := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, cause
			})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx = WithRetryNotify(ctx, func(RetryInfo) { retries++; cancel() })
			if managed {
				ctx = WithManagedRecovery(ctx)
			}
			_, err := SendWithRetry(ctx, client, SendOptions{Provider: "saved-provider"}, newDummyReq)
			if calls != 1 || retries != 0 || !errors.Is(err, cause) {
				t.Fatalf("calls=%d retries=%d err=%v", calls, retries, err)
			}
			if ClassifyRecovery(err).Retryable || DiagnoseFailure(err).Kind == "temporary" {
				t.Fatalf("permanent transport failure entered network recovery: %v", err)
			}
		})
	}
}

func TestSendWithRetryLeavesConnectionRetryToCaller(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
		}
		return statusResp(http.StatusOK, nil), nil
	})}
	_, err := SendWithRetry(t.Context(), client, SendOptions{}, newDummyReq)
	if !errors.Is(err, syscall.ECONNRESET) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	resp, err := SendWithRetry(t.Context(), client, SendOptions{}, newDummyReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 2 {
		t.Fatalf("connection reset made %d requests, want 2", calls)
	}
}
