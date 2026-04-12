package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Config holds the configuration for the localgcp server.
type Config struct {
	PortGCS           int
	PortPubSub        int
	PortSecretManager int
	PortFirestore     int
	PortCloudTasks    int
	PortVertexAI      int
	PortKMS           int
	PortLogging       int
	PortCloudRun      int
	DataDir           string
	Quiet             bool
	OllamaHost        string
	VertexModelMap    string
	VertexBackend     string // "ollama" (default), "openai", "anthropic", "stub"
	VertexAPIKey      string // API key for OpenAI/Anthropic backends
	Services          string // comma-separated orchestrated services (e.g. "spanner,bigtable")
	NoDocker          bool   // skip all orchestrated services
	PortSpanner       int
	PortBigtable      int
	PortCloudSQL      int
	PortMemorystore   int
	PortBigQuery      int
}

// DefaultConfig returns the default server configuration.
func DefaultConfig() Config {
	return Config{
		PortGCS:           4443,
		PortPubSub:        8085,
		PortSecretManager: 8086,
		PortFirestore:     8088,
		PortCloudTasks:    8089,
		PortVertexAI:      8090,
		PortKMS:           8091,
		PortLogging:       8092,
		PortCloudRun:      8093,
		PortSpanner:       9010,
		PortBigtable:      9094,
		PortCloudSQL:      5432,
		PortMemorystore:   6379,
		PortBigQuery:      9060,
		OllamaHost:        "http://localhost:11434",
	}
}

// registration pairs a Service with the port it should listen on.
type registration struct {
	service Service
	port    int
}

// Server manages the lifecycle of all emulated GCP services.
type Server struct {
	cfg           Config
	registrations []registration
	logger        *log.Logger
}

// New creates a new Server with the given configuration.
func New(cfg Config) *Server {
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if cfg.Quiet {
		logger = log.New(os.Stderr, "", 0)
	}
	return &Server{
		cfg:    cfg,
		logger: logger,
	}
}

// Register adds a service to be managed by this server.
func (s *Server) Register(svc Service, port int) {
	s.registrations = append(s.registrations, registration{service: svc, port: port})
}

// Run starts all registered services and blocks until interrupted.
// It handles graceful shutdown on SIGINT/SIGTERM.
func (s *Server) Run() error {
	if len(s.registrations) == 0 {
		return fmt.Errorf("no services registered")
	}

	// Check all ports before starting any service.
	if err := s.checkPorts(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	errCh := make(chan error, len(s.registrations))

	s.logger.Println("Starting localgcp...")
	if s.cfg.DataDir != "" {
		s.logger.Printf("  Data directory: %s", s.cfg.DataDir)
	} else {
		s.logger.Println("  Storage: in-memory (data will not persist)")
	}
	s.logger.Println()

	for _, reg := range s.registrations {
		wg.Add(1)
		go func(r registration) {
			defer wg.Done()
			addr := fmt.Sprintf(":%d", r.port)
			s.logger.Printf("  %-20s listening on %s", r.service.Name(), addr)
			if err := r.service.Start(ctx, addr); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("%s: %w", r.service.Name(), err)
			}
		}(reg)
	}

	// Small delay to let services bind, then print ready message.
	time.Sleep(100 * time.Millisecond)
	s.logger.Println()
	s.logger.Println("localgcp is ready. Press Ctrl+C to stop.")

	// Wait for either context cancellation or a service error.
	select {
	case <-ctx.Done():
		s.logger.Println()
		s.logger.Println("Shutting down...")
	case err := <-errCh:
		stop()
		return err
	}

	// Wait for all services to finish their graceful shutdown.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Println("All services stopped.")
	case <-time.After(5 * time.Second):
		s.logger.Println("Shutdown timed out after 5s, forcing exit.")
	}

	return nil
}

// checkPorts verifies that all required ports are available before starting.
func (s *Server) checkPorts() error {
	for _, reg := range s.registrations {
		addr := fmt.Sprintf(":%d", reg.port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			flag := portFlagName(reg.service.Name())
			return fmt.Errorf("port %d already in use (%s). Use %s to override",
				reg.port, reg.service.Name(), flag)
		}
		ln.Close()
	}
	return nil
}

func portFlagName(serviceName string) string {
	switch serviceName {
	case "Cloud Storage":
		return "--port-gcs"
	case "Pub/Sub":
		return "--port-pubsub"
	case "Secret Manager":
		return "--port-secretmanager"
	case "Firestore":
		return "--port-firestore"
	case "Cloud Tasks":
		return "--port-cloudtasks"
	case "Vertex AI":
		return "--port-vertexai"
	default:
		return "--port-*"
	}
}
