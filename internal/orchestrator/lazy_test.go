package orchestrator

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// mockRuntime implements ContainerRuntime for testing without Docker.
type mockRuntime struct {
	mu          sync.Mutex
	available   bool
	pullErr     error
	createErr   error
	startErr    error
	pullDelay   time.Duration
	created     []ContainerConfig
	started     []string
	stopped     []string
	removed     []string
	hostPort    string // returned by HostPort
	containerID string
	existing    map[string]bool // name -> running
}

func newMockRuntime() *mockRuntime {
	return &mockRuntime{
		available:   true,
		hostPort:    "", // set dynamically per test
		containerID: "mock-container-123",
		existing:    make(map[string]bool),
	}
}

func (m *mockRuntime) Available() bool { return m.available }

func (m *mockRuntime) Pull(ctx context.Context, img string) error {
	if m.pullDelay > 0 {
		select {
		case <-time.After(m.pullDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.pullErr
}

func (m *mockRuntime) Create(ctx context.Context, cfg ContainerConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, cfg)
	if m.createErr != nil {
		return "", m.createErr
	}
	return m.containerID, nil
}

func (m *mockRuntime) Start(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = append(m.started, id)
	return m.startErr
}

func (m *mockRuntime) Stop(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = append(m.stopped, id)
	return nil
}

func (m *mockRuntime) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removed = append(m.removed, id)
	return nil
}

func (m *mockRuntime) HostPort(ctx context.Context, id string, containerPort string) (string, error) {
	if m.hostPort == "" {
		return "", fmt.Errorf("no host port configured")
	}
	return m.hostPort, nil
}

func (m *mockRuntime) FindExisting(ctx context.Context, name string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	running, ok := m.existing[name]
	if !ok {
		return "", false, nil
	}
	return m.containerID, running, nil
}

func (m *mockRuntime) CleanupOrphans(ctx context.Context, prefix string) error { return nil }

// startEchoServer starts a TCP server that echoes data back, for proxy testing.
func startEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo server listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestLazyServiceStartsOnFirstConnection(t *testing.T) {
	echoAddr, echoCleanup := startEchoServer(t)
	defer echoCleanup()

	mock := newMockRuntime()
	mock.hostPort = echoAddr

	svc := NewLazyService("Test", ContainerConfig{
		Name:         "localgcp-test",
		Image:        "test:latest",
		InternalPort: "9999/tcp",
	}, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the lazy service on an ephemeral port.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	svcAddr := ln.Addr().String()
	ln.Close()

	go svc.Start(ctx, svcAddr)
	time.Sleep(50 * time.Millisecond) // let accept loop start

	// Verify no container started yet.
	mock.mu.Lock()
	if len(mock.started) > 0 {
		t.Fatal("container started before first connection")
	}
	mock.mu.Unlock()

	// Connect to the lazy proxy.
	conn, err := net.DialTimeout("tcp", svcAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial lazy proxy: %v", err)
	}
	defer conn.Close()

	// Send data and verify echo.
	msg := []byte("hello spanner")
	conn.Write(msg)
	buf := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf[:n]) != "hello spanner" {
		t.Fatalf("expected echo, got %q", string(buf[:n]))
	}

	// Verify container was started exactly once.
	mock.mu.Lock()
	if len(mock.started) != 1 {
		t.Fatalf("expected 1 container start, got %d", len(mock.started))
	}
	mock.mu.Unlock()
}

func TestLazyServiceReusesContainer(t *testing.T) {
	echoAddr, echoCleanup := startEchoServer(t)
	defer echoCleanup()

	mock := newMockRuntime()
	mock.hostPort = echoAddr

	svc := NewLazyService("Test", ContainerConfig{
		Name:         "localgcp-test",
		Image:        "test:latest",
		InternalPort: "9999/tcp",
	}, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	svcAddr := ln.Addr().String()
	ln.Close()

	go svc.Start(ctx, svcAddr)
	time.Sleep(50 * time.Millisecond)

	// First connection.
	conn1, _ := net.DialTimeout("tcp", svcAddr, 2*time.Second)
	conn1.Write([]byte("req1"))
	buf := make([]byte, 4)
	io.ReadFull(conn1, buf)
	conn1.Close()

	time.Sleep(50 * time.Millisecond)

	// Second connection should reuse, not create new container.
	conn2, _ := net.DialTimeout("tcp", svcAddr, 2*time.Second)
	conn2.Write([]byte("req2"))
	io.ReadFull(conn2, buf)
	conn2.Close()

	mock.mu.Lock()
	if len(mock.started) != 1 {
		t.Fatalf("expected 1 container start (reuse), got %d", len(mock.started))
	}
	mock.mu.Unlock()
}

func TestLazyServicePullFailure(t *testing.T) {
	mock := newMockRuntime()
	mock.pullErr = fmt.Errorf("registry unreachable")

	svc := NewLazyService("Test", ContainerConfig{
		Name:         "localgcp-test",
		Image:        "test:latest",
		InternalPort: "9999/tcp",
	}, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	svcAddr := ln.Addr().String()
	ln.Close()

	go svc.Start(ctx, svcAddr)
	time.Sleep(50 * time.Millisecond)

	// Connection should fail (pull error → connection dropped).
	conn, err := net.DialTimeout("tcp", svcAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	if err == nil {
		t.Fatal("expected connection to close on pull failure")
	}
	conn.Close()
}

func TestLazyServiceGracefulShutdown(t *testing.T) {
	echoAddr, echoCleanup := startEchoServer(t)
	defer echoCleanup()

	mock := newMockRuntime()
	mock.hostPort = echoAddr

	svc := NewLazyService("Test", ContainerConfig{
		Name:         "localgcp-test",
		Image:        "test:latest",
		InternalPort: "9999/tcp",
	}, mock)

	ctx, cancel := context.WithCancel(context.Background())

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	svcAddr := ln.Addr().String()
	ln.Close()

	done := make(chan error, 1)
	go func() {
		done <- svc.Start(ctx, svcAddr)
	}()
	time.Sleep(50 * time.Millisecond)

	// Trigger container start.
	conn, _ := net.DialTimeout("tcp", svcAddr, 2*time.Second)
	conn.Write([]byte("hi"))
	buf := make([]byte, 2)
	io.ReadFull(conn, buf)
	conn.Close()

	// Cancel context → should stop container and return nil.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after context cancel")
	}

	// Container should have been stopped and removed.
	mock.mu.Lock()
	if len(mock.stopped) != 1 {
		t.Fatalf("expected container stop, got %d stops", len(mock.stopped))
	}
	if len(mock.removed) != 1 {
		t.Fatalf("expected container remove, got %d removes", len(mock.removed))
	}
	mock.mu.Unlock()
}

func TestLazyServiceReusesExistingContainer(t *testing.T) {
	echoAddr, echoCleanup := startEchoServer(t)
	defer echoCleanup()

	mock := newMockRuntime()
	mock.hostPort = echoAddr
	mock.existing["localgcp-test"] = true // existing running container

	svc := NewLazyService("Test", ContainerConfig{
		Name:         "localgcp-test",
		Image:        "test:latest",
		InternalPort: "9999/tcp",
	}, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	svcAddr := ln.Addr().String()
	ln.Close()

	go svc.Start(ctx, svcAddr)
	time.Sleep(50 * time.Millisecond)

	conn, _ := net.DialTimeout("tcp", svcAddr, 2*time.Second)
	conn.Write([]byte("hi"))
	buf := make([]byte, 2)
	io.ReadFull(conn, buf)
	conn.Close()

	// Should NOT have pulled or created (reused existing).
	mock.mu.Lock()
	if len(mock.created) != 0 {
		t.Fatalf("expected 0 creates (reuse existing), got %d", len(mock.created))
	}
	if len(mock.started) != 0 {
		t.Fatalf("expected 0 starts (reuse existing), got %d", len(mock.started))
	}
	mock.mu.Unlock()
}

func TestConfigRegistry(t *testing.T) {
	for _, name := range []string{"spanner", "bigtable", "cloudsql", "memorystore"} {
		cfg, ok := ServiceRegistry[name]
		if !ok {
			t.Fatalf("missing config for %s", name)
		}
		if cfg.Image == "" {
			t.Fatalf("%s has empty image", name)
		}
		if cfg.InternalPort == "" {
			t.Fatalf("%s has empty internal port", name)
		}
		if cfg.Name == "" {
			t.Fatalf("%s has empty container name", name)
		}
	}
}

func TestMockRuntimeAvailability(t *testing.T) {
	mock := newMockRuntime()
	if !mock.Available() {
		t.Fatal("mock should be available")
	}
	mock.available = false
	if mock.Available() {
		t.Fatal("mock should be unavailable")
	}
}

func TestWithDataDir(t *testing.T) {
	// Cloud SQL supports persistence.
	cfg := WithDataDir(CloudSQLConfig, t.TempDir())
	if len(cfg.Volumes) != 1 {
		t.Fatalf("expected 1 volume mount, got %d", len(cfg.Volumes))
	}
	for _, containerPath := range cfg.Volumes {
		if containerPath != "/var/lib/postgresql/data" {
			t.Fatalf("expected postgres data path, got %s", containerPath)
		}
	}

	// Redis gets appendonly cmd when persisting.
	cfg = WithDataDir(MemorystoreConfig, t.TempDir())
	if len(cfg.Volumes) != 1 {
		t.Fatal("expected 1 volume for redis")
	}
	if len(cfg.Cmd) == 0 || cfg.Cmd[0] != "redis-server" {
		t.Fatalf("expected redis-server cmd, got %v", cfg.Cmd)
	}

	// Spanner doesn't support persistence (DataPath empty).
	cfg = WithDataDir(SpannerConfig, t.TempDir())
	if len(cfg.Volumes) != 0 {
		t.Fatal("spanner should have no volumes")
	}

	// Empty dataDir returns config unchanged.
	cfg = WithDataDir(CloudSQLConfig, "")
	if len(cfg.Volumes) != 0 {
		t.Fatal("empty dataDir should have no volumes")
	}
}

func TestWithDataDirCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "localgcp-data")

	cfg := WithDataDir(CloudSQLConfig, dataDir)
	if len(cfg.Volumes) != 1 {
		t.Fatal("expected volume mount")
	}

	// Verify the host directory was created.
	for hostPath := range cfg.Volumes {
		if _, err := os.Stat(hostPath); os.IsNotExist(err) {
			t.Fatalf("expected host directory %s to be created", hostPath)
		}
	}
}
