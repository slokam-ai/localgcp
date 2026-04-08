// Package dispatch provides a shared HTTP dispatcher with exponential backoff
// retry, used by Pub/Sub push subscriptions and Cloud Tasks.
package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Config controls retry and timeout behavior.
type Config struct {
	MaxRetries     int           // max number of retry attempts (0 = no retries)
	InitialBackoff time.Duration // initial backoff between retries
	Multiplier     float64       // backoff multiplier per retry
	MaxBackoff     time.Duration // backoff cap
	Timeout        time.Duration // per-request timeout
}

// DefaultConfig returns sensible defaults for local dev.
func DefaultConfig() Config {
	return Config{
		MaxRetries:     3,
		InitialBackoff: 1 * time.Second,
		Multiplier:     2.0,
		MaxBackoff:     10 * time.Second,
		Timeout:        30 * time.Second,
	}
}

// Result describes the outcome of a dispatch attempt.
type Result struct {
	StatusCode int
	Attempts   int
	Err        error
}

// Dispatcher sends HTTP POST requests with configurable retry.
type Dispatcher struct {
	cfg    Config
	client *http.Client
}

// New creates a Dispatcher with the given config.
func New(cfg Config) *Dispatcher {
	return &Dispatcher{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Dispatch sends body as an HTTP POST to url. It retries on non-2xx responses
// up to MaxRetries times with exponential backoff. The context controls
// cancellation of the entire dispatch (including retries).
//
//	DISPATCH RETRY FLOW
//	═══════════════════
//	POST url ──► 2xx? ──► return success
//	              │ no
//	              ▼
//	         attempts < max?
//	              │ yes         │ no
//	              ▼             ▼
//	         sleep(backoff)   return last error
//	              │
//	              ▼
//	         POST url ──► ...
func (d *Dispatcher) Dispatch(ctx context.Context, url string, body []byte, headers map[string]string) Result {
	var lastStatus int
	backoff := d.cfg.InitialBackoff

	for attempt := 0; attempt <= d.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Result{StatusCode: lastStatus, Attempts: attempt, Err: ctx.Err()}
			case <-time.After(backoff):
			}
			backoff = time.Duration(math.Min(
				float64(backoff)*d.cfg.Multiplier,
				float64(d.cfg.MaxBackoff),
			))
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return Result{Attempts: attempt + 1, Err: fmt.Errorf("build request: %w", err)}
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := d.client.Do(req)
		if err != nil {
			lastStatus = 0
			if ctx.Err() != nil {
				return Result{Attempts: attempt + 1, Err: ctx.Err()}
			}
			continue
		}
		// Drain and close body, limit read to 1MB to prevent OOM.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		lastStatus = resp.StatusCode

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return Result{StatusCode: resp.StatusCode, Attempts: attempt + 1}
		}
	}

	return Result{
		StatusCode: lastStatus,
		Attempts:   d.cfg.MaxRetries + 1,
		Err:        fmt.Errorf("dispatch failed after %d attempts, last status: %d", d.cfg.MaxRetries+1, lastStatus),
	}
}
