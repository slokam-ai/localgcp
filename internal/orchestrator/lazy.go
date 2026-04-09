package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// LazyService implements the server.Service interface for Docker-orchestrated
// emulators. It binds the port immediately but only starts the container when
// the first connection arrives.
type LazyService struct {
	name     string
	config   ContainerConfig
	runtime  ContainerRuntime
	logger   *log.Logger

	mu          sync.Mutex
	containerID string
	hostAddr    string // host:port of the running container
	started     bool
}

// NewLazyService creates a new lazy Docker-orchestrated service.
func NewLazyService(name string, cfg ContainerConfig, runtime ContainerRuntime) *LazyService {
	return &LazyService{
		name:    name,
		config:  cfg,
		runtime: runtime,
		logger:  log.New(os.Stderr, fmt.Sprintf("[%s] ", name), log.LstdFlags),
	}
}

func (s *LazyService) Name() string { return s.name }

// Start binds the TCP listener and enters the accept loop. It blocks until
// ctx is cancelled (matching the server.Service contract).
func (s *LazyService) Start(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// Close listener when context cancels.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	s.logger.Printf("Lazy proxy listening on %s (container starts on first connection)", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				// Context cancelled, clean shutdown.
				s.shutdown()
				return nil
			}
			continue
		}
		go s.handleConn(ctx, conn)
	}
}

// handleConn ensures the container is running, then proxies the connection.
func (s *LazyService) handleConn(ctx context.Context, clientConn net.Conn) {
	defer clientConn.Close()

	// Ensure container is started.
	hostAddr, err := s.ensureContainer(ctx)
	if err != nil {
		s.logger.Printf("Failed to start container: %v", err)
		return
	}

	// Connect to the container.
	containerConn, err := net.DialTimeout("tcp", hostAddr, 5*time.Second)
	if err != nil {
		s.logger.Printf("Failed to connect to container at %s: %v", hostAddr, err)
		return
	}
	defer containerConn.Close()

	// Bidirectional proxy. When either side closes, both goroutines exit.
	done := make(chan struct{})
	go func() {
		io.Copy(containerConn, clientConn)
		containerConn.(*net.TCPConn).CloseWrite()
		close(done)
	}()
	io.Copy(clientConn, containerConn)
	clientConn.(*net.TCPConn).CloseWrite()
	<-done
}

// ensureContainer starts the Docker container if not already running.
// Thread-safe: only one goroutine starts the container.
func (s *LazyService) ensureContainer(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return s.hostAddr, nil
	}

	// Check for existing container from a previous session.
	if id, running, err := s.runtime.FindExisting(ctx, s.config.Name); err == nil && id != "" {
		if running {
			s.logger.Printf("Reusing existing container %s", s.config.Name)
			hostAddr, err := s.runtime.HostPort(ctx, id, s.config.InternalPort)
			if err == nil {
				s.containerID = id
				s.hostAddr = hostAddr
				s.started = true
				return s.hostAddr, nil
			}
		}
		// Container exists but not running, remove it.
		s.runtime.Remove(ctx, id)
	}

	// Pull image (with timeout).
	pullCtx, pullCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer pullCancel()
	if err := s.runtime.Pull(pullCtx, s.config.Image); err != nil {
		return "", fmt.Errorf("pull %s: %w", s.config.Image, err)
	}

	// Create container.
	id, err := s.runtime.Create(ctx, s.config)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", s.config.Name, err)
	}
	s.containerID = id

	// Start container.
	startCtx, startCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startCancel()
	if err := s.runtime.Start(startCtx, id); err != nil {
		s.runtime.Remove(ctx, id)
		return "", fmt.Errorf("start %s: %w", s.config.Name, err)
	}

	// Get the dynamically assigned host port.
	hostAddr, err := s.runtime.HostPort(ctx, id, s.config.InternalPort)
	if err != nil {
		s.runtime.Stop(ctx, id)
		s.runtime.Remove(ctx, id)
		return "", fmt.Errorf("host port %s: %w", s.config.Name, err)
	}

	// Health check: wait for TCP readiness.
	if err := s.waitForReady(ctx, hostAddr); err != nil {
		s.runtime.Stop(ctx, id)
		s.runtime.Remove(ctx, id)
		return "", fmt.Errorf("health check %s: %w", s.config.Name, err)
	}

	s.hostAddr = hostAddr
	s.started = true
	s.logger.Printf("Container ready at %s", hostAddr)
	return s.hostAddr, nil
}

// waitForReady polls TCP until the port is accepting connections.
func (s *LazyService) waitForReady(ctx context.Context, addr string) error {
	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s to become ready", addr)
}

// shutdown stops and removes the container if running.
func (s *LazyService) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started || s.containerID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	s.logger.Printf("Stopping container %s", s.config.Name)
	s.runtime.Stop(ctx, s.containerID)
	s.runtime.Remove(ctx, s.containerID)
	s.started = false
}
