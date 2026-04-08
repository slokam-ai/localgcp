package dispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatchSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := New(DefaultConfig())
	result := d.Dispatch(context.Background(), srv.URL, []byte(`{"msg":"hi"}`), nil)
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", result.StatusCode)
	}
	if result.Attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", result.Attempts)
	}
}

func TestDispatchRetryThenSuccess(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		Multiplier:     1.0, // flat backoff for fast tests
		MaxBackoff:     100 * time.Millisecond,
		Timeout:        5 * time.Second,
	}
	d := New(cfg)
	result := d.Dispatch(context.Background(), srv.URL, []byte(`{}`), nil)
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", result.StatusCode)
	}
	if result.Attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", result.Attempts)
	}
}

func TestDispatchAllRetriesExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cfg := Config{
		MaxRetries:     2,
		InitialBackoff: 10 * time.Millisecond,
		Multiplier:     1.0,
		MaxBackoff:     100 * time.Millisecond,
		Timeout:        5 * time.Second,
	}
	d := New(cfg)
	result := d.Dispatch(context.Background(), srv.URL, []byte(`{}`), nil)
	if result.Err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if result.StatusCode != 503 {
		t.Fatalf("expected last status 503, got %d", result.StatusCode)
	}
	if result.Attempts != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), got %d", result.Attempts)
	}
}

func TestDispatchContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := Config{
		MaxRetries:     10,
		InitialBackoff: 1 * time.Second, // long backoff so cancellation kicks in
		Multiplier:     1.0,
		MaxBackoff:     5 * time.Second,
		Timeout:        5 * time.Second,
	}
	d := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := d.Dispatch(ctx, srv.URL, []byte(`{}`), nil)
	if result.Err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", result.Err)
	}
	// 1 attempt completed (got 500), then cancelled during 1s retry backoff.
	if result.Attempts < 1 {
		t.Fatalf("expected at least 1 attempt before cancellation, got %d", result.Attempts)
	}
}

func TestDispatchCustomHeaders(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := New(DefaultConfig())
	d.Dispatch(context.Background(), srv.URL, []byte(`{}`), map[string]string{
		"X-Custom": "test-value",
	})
	if gotHeader != "test-value" {
		t.Fatalf("expected X-Custom=test-value, got %q", gotHeader)
	}
}

func TestDispatchNoRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.MaxRetries = 0
	d := New(cfg)
	result := d.Dispatch(context.Background(), srv.URL, []byte(`{}`), nil)
	if result.Err == nil {
		t.Fatal("expected error with no retries on 502")
	}
	if result.Attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", result.Attempts)
	}
}

func TestDispatchResponseBodyLimited(t *testing.T) {
	// Server sends a large response body. Dispatcher should not OOM.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write 2MB of data.
		data := make([]byte, 2<<20)
		w.Write(data)
	}))
	defer srv.Close()

	d := New(DefaultConfig())
	result := d.Dispatch(context.Background(), srv.URL, []byte(`{}`), nil)
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
}
