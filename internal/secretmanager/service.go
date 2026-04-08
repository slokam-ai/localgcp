package secretmanager

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Service implements the Secret Manager emulator.
type Service struct {
	secretmanagerpb.UnimplementedSecretManagerServiceServer

	dataDir string
	quiet   bool
	logger  *log.Logger
	store   *Store
}

// New creates a new Secret Manager service.
func New(dataDir string, quiet bool) *Service {
	logger := log.New(os.Stderr, "[secretmanager] ", log.LstdFlags)
	return &Service{
		dataDir: dataDir,
		quiet:   quiet,
		logger:  logger,
		store:   NewStore(dataDir),
	}
}

func (s *Service) Name() string { return "Secret Manager" }

func (s *Service) Start(ctx context.Context, addr string) error {
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
	)
	secretmanagerpb.RegisterSecretManagerServiceServer(srv, s)
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

func (s *Service) loggingInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
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

// --- Resource name parsing helpers ---

// parseSecretName extracts project and secret ID from "projects/{project}/secrets/{secret}".
func parseSecretName(name string) (project, secretID string, err error) {
	parts := strings.Split(name, "/")
	if len(parts) == 4 && parts[0] == "projects" && parts[2] == "secrets" {
		return parts[1], parts[3], nil
	}
	return "", "", fmt.Errorf("invalid secret name: %s", name)
}

// parseVersionName extracts project, secret ID, and version from
// "projects/{project}/secrets/{secret}/versions/{version}".
func parseVersionName(name string) (secretName, versionID string, err error) {
	parts := strings.Split(name, "/")
	if len(parts) == 6 && parts[0] == "projects" && parts[2] == "secrets" && parts[4] == "versions" {
		secretName = strings.Join(parts[:4], "/")
		return secretName, parts[5], nil
	}
	return "", "", fmt.Errorf("invalid version name: %s", name)
}

// --- gRPC method implementations ---

func (s *Service) CreateSecret(ctx context.Context, req *secretmanagerpb.CreateSecretRequest) (*secretmanagerpb.Secret, error) {
	if req.GetParent() == "" {
		return nil, status.Error(codes.InvalidArgument, "parent is required")
	}
	if req.GetSecretId() == "" {
		return nil, status.Error(codes.InvalidArgument, "secret_id is required")
	}

	var labels map[string]string
	if req.GetSecret() != nil {
		labels = req.GetSecret().GetLabels()
	}

	secret, err := s.store.CreateSecret(req.GetParent(), req.GetSecretId(), labels)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, status.Errorf(codes.AlreadyExists, "Secret [%s/secrets/%s] already exists", req.GetParent(), req.GetSecretId())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return secret, nil
}

func (s *Service) GetSecret(ctx context.Context, req *secretmanagerpb.GetSecretRequest) (*secretmanagerpb.Secret, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	secret, err := s.store.GetSecret(req.GetName())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Secret [%s] not found", req.GetName())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return secret, nil
}

func (s *Service) ListSecrets(ctx context.Context, req *secretmanagerpb.ListSecretsRequest) (*secretmanagerpb.ListSecretsResponse, error) {
	if req.GetParent() == "" {
		return nil, status.Error(codes.InvalidArgument, "parent is required")
	}

	secrets := s.store.ListSecrets(req.GetParent())
	return &secretmanagerpb.ListSecretsResponse{
		Secrets:   secrets,
		TotalSize: int32(len(secrets)),
	}, nil
}

func (s *Service) DeleteSecret(ctx context.Context, req *secretmanagerpb.DeleteSecretRequest) (*emptypb.Empty, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	err := s.store.DeleteSecret(req.GetName())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Secret [%s] not found", req.GetName())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *Service) AddSecretVersion(ctx context.Context, req *secretmanagerpb.AddSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	if req.GetParent() == "" {
		return nil, status.Error(codes.InvalidArgument, "parent is required")
	}
	if req.GetPayload() == nil {
		return nil, status.Error(codes.InvalidArgument, "payload is required")
	}

	version, err := s.store.AddSecretVersion(req.GetParent(), req.GetPayload().GetData())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Secret [%s] not found", req.GetParent())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return version, nil
}

func (s *Service) GetSecretVersion(ctx context.Context, req *secretmanagerpb.GetSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	secretName, versionID, err := parseVersionName(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	version, err := s.store.GetSecretVersion(secretName, versionID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "SecretVersion [%s] not found", req.GetName())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return version, nil
}

func (s *Service) AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	secretName, versionID, err := parseVersionName(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	version, payload, err := s.store.AccessSecretVersion(secretName, versionID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "SecretVersion [%s] not found", req.GetName())
		}
		if strings.Contains(err.Error(), "failed precondition") {
			return nil, status.Errorf(codes.FailedPrecondition, "SecretVersion [%s] is not enabled", req.GetName())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &secretmanagerpb.AccessSecretVersionResponse{
		Name: version.Name,
		Payload: &secretmanagerpb.SecretPayload{
			Data: payload,
		},
	}, nil
}

func (s *Service) ListSecretVersions(ctx context.Context, req *secretmanagerpb.ListSecretVersionsRequest) (*secretmanagerpb.ListSecretVersionsResponse, error) {
	if req.GetParent() == "" {
		return nil, status.Error(codes.InvalidArgument, "parent is required")
	}

	versions, err := s.store.ListSecretVersions(req.GetParent())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Secret [%s] not found", req.GetParent())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &secretmanagerpb.ListSecretVersionsResponse{
		Versions:  versions,
		TotalSize: int32(len(versions)),
	}, nil
}

func (s *Service) EnableSecretVersion(ctx context.Context, req *secretmanagerpb.EnableSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	secretName, versionID, err := parseVersionName(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	version, err := s.store.EnableSecretVersion(secretName, versionID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "SecretVersion [%s] not found", req.GetName())
		}
		if strings.Contains(err.Error(), "failed precondition") {
			return nil, status.Errorf(codes.FailedPrecondition, "SecretVersion [%s] is destroyed and cannot be enabled", req.GetName())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return version, nil
}

func (s *Service) DisableSecretVersion(ctx context.Context, req *secretmanagerpb.DisableSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	secretName, versionID, err := parseVersionName(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	version, err := s.store.DisableSecretVersion(secretName, versionID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "SecretVersion [%s] not found", req.GetName())
		}
		if strings.Contains(err.Error(), "failed precondition") {
			return nil, status.Errorf(codes.FailedPrecondition, "SecretVersion [%s] is destroyed and cannot be disabled", req.GetName())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return version, nil
}

func (s *Service) DestroySecretVersion(ctx context.Context, req *secretmanagerpb.DestroySecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	secretName, versionID, err := parseVersionName(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	version, err := s.store.DestroySecretVersion(secretName, versionID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "SecretVersion [%s] not found", req.GetName())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return version, nil
}

// UpdateSecret returns Unimplemented.
func (s *Service) UpdateSecret(ctx context.Context, req *secretmanagerpb.UpdateSecretRequest) (*secretmanagerpb.Secret, error) {
	return nil, status.Error(codes.Unimplemented, "localgcp: UpdateSecret not yet supported")
}
