package kms

import (
	"context"
	"crypto"
	"fmt"
	"log"
	"net"
	"os"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements the Cloud KMS emulator.
type Service struct {
	kmspb.UnimplementedKeyManagementServiceServer

	dataDir string
	quiet   bool
	logger  *log.Logger
	store   *Store
}

// New creates a new Cloud KMS service.
func New(dataDir string, quiet bool) *Service {
	logger := log.New(os.Stderr, "[kms] ", log.LstdFlags)
	return &Service{
		dataDir: dataDir,
		quiet:   quiet,
		logger:  logger,
		store:   NewStore(),
	}
}

func (s *Service) Name() string { return "Cloud KMS" }

func (s *Service) Start(ctx context.Context, addr string) error {
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
	)
	kmspb.RegisterKeyManagementServiceServer(srv, s)
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

// --- KeyRing RPCs ---

func (s *Service) CreateKeyRing(_ context.Context, req *kmspb.CreateKeyRingRequest) (*kmspb.KeyRing, error) {
	name := req.GetParent() + "/keyRings/" + req.GetKeyRingId()
	kr, err := s.store.CreateKeyRing(name)
	if err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "KeyRing %s already exists", name)
	}
	return &kmspb.KeyRing{Name: kr.Name, CreateTime: kr.CreateTime}, nil
}

func (s *Service) GetKeyRing(_ context.Context, req *kmspb.GetKeyRingRequest) (*kmspb.KeyRing, error) {
	kr, ok := s.store.GetKeyRing(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "KeyRing %s not found", req.GetName())
	}
	return &kmspb.KeyRing{Name: kr.Name, CreateTime: kr.CreateTime}, nil
}

func (s *Service) ListKeyRings(_ context.Context, req *kmspb.ListKeyRingsRequest) (*kmspb.ListKeyRingsResponse, error) {
	rings := s.store.ListKeyRings(req.GetParent())
	var result []*kmspb.KeyRing
	for _, kr := range rings {
		result = append(result, &kmspb.KeyRing{Name: kr.Name, CreateTime: kr.CreateTime})
	}
	return &kmspb.ListKeyRingsResponse{KeyRings: result, TotalSize: int32(len(result))}, nil
}

// --- CryptoKey RPCs ---

func (s *Service) CreateCryptoKey(_ context.Context, req *kmspb.CreateCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	name := req.GetParent() + "/cryptoKeys/" + req.GetCryptoKeyId()
	ck := req.GetCryptoKey()

	purpose := ck.GetPurpose()
	var algorithm kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm
	if tmpl := ck.GetVersionTemplate(); tmpl != nil {
		algorithm = tmpl.GetAlgorithm()
	}

	stored, ver, err := s.store.CreateCryptoKey(name, purpose, algorithm)
	if err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "CryptoKey %s already exists", name)
	}
	return s.toProtoCryptoKey(stored, ver), nil
}

func (s *Service) GetCryptoKey(_ context.Context, req *kmspb.GetCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	ck, ok := s.store.GetCryptoKey(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found", req.GetName())
	}
	ver, _ := s.store.PrimaryVersion(ck.Name)
	return s.toProtoCryptoKey(ck, ver), nil
}

func (s *Service) ListCryptoKeys(_ context.Context, req *kmspb.ListCryptoKeysRequest) (*kmspb.ListCryptoKeysResponse, error) {
	keys := s.store.ListCryptoKeys(req.GetParent())
	var result []*kmspb.CryptoKey
	for _, ck := range keys {
		ver, _ := s.store.PrimaryVersion(ck.Name)
		result = append(result, s.toProtoCryptoKey(ck, ver))
	}
	return &kmspb.ListCryptoKeysResponse{CryptoKeys: result, TotalSize: int32(len(result))}, nil
}

// --- CryptoKeyVersion RPCs ---

func (s *Service) GetCryptoKeyVersion(_ context.Context, req *kmspb.GetCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	v, ok := s.store.GetVersion(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", req.GetName())
	}
	return s.toProtoVersion(v), nil
}

func (s *Service) ListCryptoKeyVersions(_ context.Context, req *kmspb.ListCryptoKeyVersionsRequest) (*kmspb.ListCryptoKeyVersionsResponse, error) {
	versions := s.store.ListVersions(req.GetParent())
	var result []*kmspb.CryptoKeyVersion
	for _, v := range versions {
		result = append(result, s.toProtoVersion(v))
	}
	return &kmspb.ListCryptoKeyVersionsResponse{CryptoKeyVersions: result, TotalSize: int32(len(result))}, nil
}

func (s *Service) DestroyCryptoKeyVersion(_ context.Context, req *kmspb.DestroyCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	v, err := s.store.DestroyVersion(req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", req.GetName())
	}
	return s.toProtoVersion(v), nil
}

// --- Crypto operation RPCs ---

func (s *Service) Encrypt(_ context.Context, req *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	keyName := req.GetName()
	// Resolve to primary version.
	ver, ok := s.store.PrimaryVersion(keyName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found or has no primary version", keyName)
	}

	ciphertext, err := s.store.Encrypt(ver.Name, req.GetPlaintext())
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "encrypt: %v", err)
	}

	return &kmspb.EncryptResponse{
		Name:       ver.Name,
		Ciphertext: ciphertext,
	}, nil
}

func (s *Service) Decrypt(_ context.Context, req *kmspb.DecryptRequest) (*kmspb.DecryptResponse, error) {
	keyName := req.GetName()
	ver, ok := s.store.PrimaryVersion(keyName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found or has no primary version", keyName)
	}

	plaintext, err := s.store.Decrypt(ver.Name, req.GetCiphertext())
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "decrypt: %v", err)
	}

	return &kmspb.DecryptResponse{Plaintext: plaintext}, nil
}

func (s *Service) AsymmetricSign(_ context.Context, req *kmspb.AsymmetricSignRequest) (*kmspb.AsymmetricSignResponse, error) {
	versionName := req.GetName()
	if _, ok := s.store.GetVersion(versionName); !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", versionName)
	}

	var digest []byte
	var hashType crypto.Hash
	d := req.GetDigest()
	switch {
	case d.GetSha256() != nil:
		digest = d.GetSha256()
		hashType = crypto.SHA256
	case d.GetSha384() != nil:
		digest = d.GetSha384()
		hashType = crypto.SHA384
	case d.GetSha512() != nil:
		digest = d.GetSha512()
		hashType = crypto.SHA512
	default:
		return nil, status.Errorf(codes.InvalidArgument, "digest is required")
	}

	sig, err := s.store.AsymmetricSign(versionName, digest, hashType)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "sign: %v", err)
	}

	return &kmspb.AsymmetricSignResponse{
		Signature: sig,
		Name:      versionName,
	}, nil
}

func (s *Service) GetPublicKey(_ context.Context, req *kmspb.GetPublicKeyRequest) (*kmspb.PublicKey, error) {
	versionName := req.GetName()
	ver, ok := s.store.GetVersion(versionName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", versionName)
	}

	pemStr, err := s.store.GetPublicKeyPEM(versionName)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "get public key: %v", err)
	}

	return &kmspb.PublicKey{
		Pem:       pemStr,
		Algorithm: ver.Algorithm,
		Name:      versionName,
	}, nil
}

func (s *Service) MacSign(_ context.Context, req *kmspb.MacSignRequest) (*kmspb.MacSignResponse, error) {
	versionName := req.GetName()

	mac, err := s.store.MacSign(versionName, req.GetData())
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "mac sign: %v", err)
	}

	return &kmspb.MacSignResponse{
		Mac:  mac,
		Name: versionName,
	}, nil
}

func (s *Service) MacVerify(_ context.Context, req *kmspb.MacVerifyRequest) (*kmspb.MacVerifyResponse, error) {
	versionName := req.GetName()

	expected, err := s.store.MacSign(versionName, req.GetData())
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "mac verify: %v", err)
	}

	success := len(expected) == len(req.GetMac())
	if success {
		for i := range expected {
			if expected[i] != req.GetMac()[i] {
				success = false
				break
			}
		}
	}

	return &kmspb.MacVerifyResponse{
		Name:                 versionName,
		Success:              success,
	}, nil
}

// --- Proto conversion helpers ---

func (s *Service) toProtoCryptoKey(ck *storedCryptoKey, primary *storedVersion) *kmspb.CryptoKey {
	result := &kmspb.CryptoKey{
		Name:       ck.Name,
		Purpose:    ck.Purpose,
		CreateTime: ck.CreateTime,
		VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
			Algorithm: ck.Algorithm,
		},
	}
	if primary != nil {
		result.Primary = s.toProtoVersion(primary)
	}
	return result
}

func (s *Service) toProtoVersion(v *storedVersion) *kmspb.CryptoKeyVersion {
	return &kmspb.CryptoKeyVersion{
		Name:       v.Name,
		State:      v.State,
		Algorithm:  v.Algorithm,
		CreateTime: v.CreateTime,
	}
}

// --- Logging ---

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

// Suppress unused variable warnings.
var _ = timestamppb.Now
