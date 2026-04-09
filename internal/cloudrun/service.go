package cloudrun

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"

	"cloud.google.com/go/run/apiv2/runpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/protobuf/types/known/anypb"
)

// Service implements the Cloud Run emulator (Services API).
type Service struct {
	runpb.UnimplementedServicesServer

	dataDir string
	quiet   bool
	logger  *log.Logger
	store   *Store
}

// New creates a new Cloud Run service.
func New(dataDir string, quiet bool) *Service {
	logger := log.New(os.Stderr, "[cloudrun] ", log.LstdFlags)
	return &Service{
		dataDir: dataDir,
		quiet:   quiet,
		logger:  logger,
		store:   NewStore(),
	}
}

func (s *Service) Name() string { return "Cloud Run" }

func (s *Service) Start(ctx context.Context, addr string) error {
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
	)
	runpb.RegisterServicesServer(srv, s)
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

func (s *Service) CreateService(_ context.Context, req *runpb.CreateServiceRequest) (*longrunning.Operation, error) {
	parent := req.GetParent()
	serviceID := req.GetServiceId()
	name := parent + "/services/" + serviceID

	svc, err := s.store.Create(name, req.GetService())
	if err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "Service %s already exists", name)
	}

	return completedOp(name+"/operations/create", svc)
}

func (s *Service) GetService(_ context.Context, req *runpb.GetServiceRequest) (*runpb.Service, error) {
	svc, ok := s.store.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Service %s not found", req.GetName())
	}
	return svc, nil
}

func (s *Service) ListServices(_ context.Context, req *runpb.ListServicesRequest) (*runpb.ListServicesResponse, error) {
	services := s.store.List(req.GetParent())
	return &runpb.ListServicesResponse{Services: services}, nil
}

func (s *Service) UpdateService(_ context.Context, req *runpb.UpdateServiceRequest) (*longrunning.Operation, error) {
	svc := req.GetService()
	name := svc.GetName()

	updated, err := s.store.Update(name, svc)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "Service %s not found", name)
	}

	return completedOp(name+"/operations/update", updated)
}

func (s *Service) DeleteService(_ context.Context, req *runpb.DeleteServiceRequest) (*longrunning.Operation, error) {
	name := req.GetName()
	if !s.store.Delete(name) {
		return nil, status.Errorf(codes.NotFound, "Service %s not found", name)
	}

	return completedOp(name+"/operations/delete", &runpb.Service{Name: name})
}

// completedOp wraps a result in an immediately-completed long-running operation.
func completedOp(opName string, result *runpb.Service) (*longrunning.Operation, error) {
	any, err := anypb.New(result)
	if err != nil {
		return nil, fmt.Errorf("marshal operation result: %w", err)
	}
	return &longrunning.Operation{
		Name: opName,
		Done: true,
		Result: &longrunning.Operation_Response{Response: any},
	}, nil
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
