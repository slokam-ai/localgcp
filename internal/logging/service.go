package logging

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Service implements the Cloud Logging emulator.
type Service struct {
	loggingpb.UnimplementedLoggingServiceV2Server

	dataDir string
	quiet   bool
	logger  *log.Logger
	store   *Store
}

// New creates a new Cloud Logging service.
func New(dataDir string, quiet bool) *Service {
	logger := log.New(os.Stderr, "[logging] ", log.LstdFlags)
	return &Service{
		dataDir: dataDir,
		quiet:   quiet,
		logger:  logger,
		store:   NewStore(),
	}
}

func (s *Service) Name() string { return "Cloud Logging" }

func (s *Service) Start(ctx context.Context, addr string) error {
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
	)
	loggingpb.RegisterLoggingServiceV2Server(srv, s)
	reflection.Register(srv)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	if err := srv.Serve(ln); err != nil {
		return err
	}
	return nil
}

func (s *Service) WriteLogEntries(_ context.Context, req *loggingpb.WriteLogEntriesRequest) (*loggingpb.WriteLogEntriesResponse, error) {
	entries := req.GetEntries()

	// Fill in defaults from the request-level fields.
	logName := req.GetLogName()
	resource := req.GetResource()
	labels := req.GetLabels()

	for _, entry := range entries {
		if entry.GetLogName() == "" && logName != "" {
			entry.LogName = logName
		}
		if entry.GetResource() == nil && resource != nil {
			entry.Resource = resource
		}
		if len(entry.GetLabels()) == 0 && len(labels) > 0 {
			entry.Labels = labels
		}
	}

	s.store.Write(entries)
	return &loggingpb.WriteLogEntriesResponse{}, nil
}

func (s *Service) ListLogEntries(_ context.Context, req *loggingpb.ListLogEntriesRequest) (*loggingpb.ListLogEntriesResponse, error) {
	entries := s.store.List(req.GetResourceNames(), req.GetFilter(), int(req.GetPageSize()))
	return &loggingpb.ListLogEntriesResponse{Entries: entries}, nil
}

func (s *Service) ListLogs(_ context.Context, req *loggingpb.ListLogsRequest) (*loggingpb.ListLogsResponse, error) {
	logs := s.store.ListLogs(req.GetParent())
	return &loggingpb.ListLogsResponse{LogNames: logs}, nil
}

func (s *Service) DeleteLog(_ context.Context, req *loggingpb.DeleteLogRequest) (*emptypb.Empty, error) {
	s.store.DeleteLog(req.GetLogName())
	return &emptypb.Empty{}, nil
}

func (s *Service) loggingInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	resp, err := handler(ctx, req)
	if !s.quiet {
		code := codes.OK
		if err != nil {
			code = status.Code(err)
		}
		s.logger.Printf("%s %s", info.FullMethod, code)
	}
	return resp, err
}
