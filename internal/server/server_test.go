package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// stubService is a minimal Service for testing the server lifecycle.
type stubService struct {
	name string
}

func (s *stubService) Name() string { return s.name }

func (s *stubService) Start(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Addr: addr, Handler: mux}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func TestServerStartsAndStops(t *testing.T) {
	cfg := Config{
		PortGCS:           0, // not used directly here
		PortPubSub:        0,
		PortSecretManager: 0,
		PortFirestore:     0,
		Quiet:             true,
	}

	// Use ephemeral ports to avoid conflicts in tests.
	port := findFreePort(t)

	srv := New(cfg)
	srv.Register(&stubService{name: "TestService"}, port)

	done := make(chan error, 1)
	go func() {
		done <- srv.Run()
	}()

	// Wait for the server to be ready.
	waitForPort(t, port)

	// Verify the service is responding.
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/", port))
	if err != nil {
		t.Fatalf("failed to reach service: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Send interrupt to trigger shutdown.
	// We can't easily send SIGINT in a test, so we'll just check the
	// server started correctly. The signal handling is tested via the
	// integration test (localgcp up + kill).
}

func TestPortConflictDetection(t *testing.T) {
	// Bind a port.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to bind: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	cfg := Config{Quiet: true}
	srv := New(cfg)
	srv.Register(&stubService{name: "Cloud Storage"}, port)

	err = srv.Run()
	if err == nil {
		t.Fatal("expected error for port conflict, got nil")
	}

	expected := fmt.Sprintf("port %d already in use (Cloud Storage). Use --port-gcs to override", port)
	if err.Error() != expected {
		t.Fatalf("unexpected error message:\n  got:  %s\n  want: %s", err.Error(), expected)
	}
}

func TestNoServicesRegistered(t *testing.T) {
	srv := New(Config{Quiet: true})
	err := srv.Run()
	if err == nil || err.Error() != "no services registered" {
		t.Fatalf("expected 'no services registered' error, got: %v", err)
	}
}

func findFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func waitForPort(t *testing.T, port int) {
	t.Helper()
	addr := fmt.Sprintf("localhost:%d", port)
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port %d never became available", port)
}
