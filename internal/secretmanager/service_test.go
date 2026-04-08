package secretmanager

import (
	"context"
	"fmt"
	"net"
	"testing"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// startTestServer starts a Secret Manager gRPC server on an ephemeral port
// and returns a connected client. The server is shut down when the test ends.
func startTestServer(t *testing.T) secretmanagerpb.SecretManagerServiceClient {
	t.Helper()

	svc := New("", true)

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	srv := grpc.NewServer()
	secretmanagerpb.RegisterSecretManagerServiceServer(srv, svc)

	go srv.Serve(ln)
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient(
		ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return secretmanagerpb.NewSecretManagerServiceClient(conn)
}

func TestCreateAndGetSecret(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	// Create a secret.
	secret, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "my-secret",
		Secret: &secretmanagerpb.Secret{
			Labels: map[string]string{"env": "test"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	if secret.Name != "projects/test-project/secrets/my-secret" {
		t.Fatalf("unexpected name: %s", secret.Name)
	}
	if secret.Labels["env"] != "test" {
		t.Fatalf("unexpected labels: %v", secret.Labels)
	}
	if secret.CreateTime == nil {
		t.Fatal("expected create_time to be set")
	}

	// Get the same secret.
	got, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{
		Name: "projects/test-project/secrets/my-secret",
	})
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Name != secret.Name {
		t.Fatalf("get returned different name: %s vs %s", got.Name, secret.Name)
	}
}

func TestCreateDuplicateSecret(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	req := &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "dup-secret",
		Secret:   &secretmanagerpb.Secret{},
	}

	_, err := client.CreateSecret(ctx, req)
	if err != nil {
		t.Fatalf("first CreateSecret: %v", err)
	}

	_, err = client.CreateSecret(ctx, req)
	if err == nil {
		t.Fatal("expected error for duplicate secret")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v", err)
	}
}

func TestGetNonexistentSecret(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	_, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{
		Name: "projects/test-project/secrets/no-such-secret",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent secret")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestListSecrets(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	// Create two secrets.
	for _, id := range []string{"alpha", "beta"} {
		_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
			Parent:   "projects/test-project",
			SecretId: id,
			Secret:   &secretmanagerpb.Secret{},
		})
		if err != nil {
			t.Fatalf("CreateSecret(%s): %v", id, err)
		}
	}

	resp, err := client.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{
		Parent: "projects/test-project",
	})
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(resp.Secrets) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(resp.Secrets))
	}
	if resp.TotalSize != 2 {
		t.Fatalf("expected total_size 2, got %d", resp.TotalSize)
	}
}

func TestDeleteSecret(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "to-delete",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	_, err = client.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{
		Name: "projects/test-project/secrets/to-delete",
	})
	if err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}

	// Verify it's gone.
	_, err = client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{
		Name: "projects/test-project/secrets/to-delete",
	})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound after delete, got %v", err)
	}
}

func TestAddAndAccessVersion(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	secretName := "projects/test-project/secrets/versioned"
	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "versioned",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	// Add a version.
	v1, err := client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent: secretName,
		Payload: &secretmanagerpb.SecretPayload{
			Data: []byte("secret-value-1"),
		},
	})
	if err != nil {
		t.Fatalf("AddSecretVersion: %v", err)
	}
	if v1.Name != secretName+"/versions/1" {
		t.Fatalf("unexpected version name: %s", v1.Name)
	}
	if v1.State != secretmanagerpb.SecretVersion_ENABLED {
		t.Fatalf("expected ENABLED state, got %s", v1.State)
	}

	// Access the version.
	resp, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: secretName + "/versions/1",
	})
	if err != nil {
		t.Fatalf("AccessSecretVersion: %v", err)
	}
	if string(resp.Payload.Data) != "secret-value-1" {
		t.Fatalf("unexpected payload: %q", string(resp.Payload.Data))
	}
}

func TestLatestVersionAlias(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	secretName := "projects/test-project/secrets/latest-test"
	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "latest-test",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	// Add two versions.
	for i := 1; i <= 2; i++ {
		_, err := client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
			Parent: secretName,
			Payload: &secretmanagerpb.SecretPayload{
				Data: []byte(fmt.Sprintf("value-%d", i)),
			},
		})
		if err != nil {
			t.Fatalf("AddSecretVersion(%d): %v", i, err)
		}
	}

	// Access "latest" should return version 2.
	resp, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: secretName + "/versions/latest",
	})
	if err != nil {
		t.Fatalf("AccessSecretVersion(latest): %v", err)
	}
	if string(resp.Payload.Data) != "value-2" {
		t.Fatalf("expected value-2, got %q", string(resp.Payload.Data))
	}
	if resp.Name != secretName+"/versions/2" {
		t.Fatalf("expected versions/2 in name, got %s", resp.Name)
	}
}

func TestListSecretVersions(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	secretName := "projects/test-project/secrets/list-versions"
	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "list-versions",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	for i := 0; i < 3; i++ {
		_, err := client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
			Parent: secretName,
			Payload: &secretmanagerpb.SecretPayload{
				Data: []byte(fmt.Sprintf("v%d", i)),
			},
		})
		if err != nil {
			t.Fatalf("AddSecretVersion: %v", err)
		}
	}

	resp, err := client.ListSecretVersions(ctx, &secretmanagerpb.ListSecretVersionsRequest{
		Parent: secretName,
	})
	if err != nil {
		t.Fatalf("ListSecretVersions: %v", err)
	}
	if len(resp.Versions) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(resp.Versions))
	}
	if resp.TotalSize != 3 {
		t.Fatalf("expected total_size 3, got %d", resp.TotalSize)
	}
}

func TestDisableVersion(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	secretName := "projects/test-project/secrets/disable-test"
	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "disable-test",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	_, err = client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte("data")},
	})
	if err != nil {
		t.Fatalf("AddSecretVersion: %v", err)
	}

	versionName := secretName + "/versions/1"

	// Disable.
	v, err := client.DisableSecretVersion(ctx, &secretmanagerpb.DisableSecretVersionRequest{
		Name: versionName,
	})
	if err != nil {
		t.Fatalf("DisableSecretVersion: %v", err)
	}
	if v.State != secretmanagerpb.SecretVersion_DISABLED {
		t.Fatalf("expected DISABLED, got %s", v.State)
	}

	// Access disabled version should fail with FailedPrecondition.
	_, err = client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: versionName,
	})
	if err == nil {
		t.Fatal("expected error accessing disabled version")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestEnableVersion(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	secretName := "projects/test-project/secrets/enable-test"
	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "enable-test",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	_, err = client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte("data")},
	})
	if err != nil {
		t.Fatalf("AddSecretVersion: %v", err)
	}

	versionName := secretName + "/versions/1"

	// Disable then re-enable.
	_, err = client.DisableSecretVersion(ctx, &secretmanagerpb.DisableSecretVersionRequest{
		Name: versionName,
	})
	if err != nil {
		t.Fatalf("DisableSecretVersion: %v", err)
	}

	v, err := client.EnableSecretVersion(ctx, &secretmanagerpb.EnableSecretVersionRequest{
		Name: versionName,
	})
	if err != nil {
		t.Fatalf("EnableSecretVersion: %v", err)
	}
	if v.State != secretmanagerpb.SecretVersion_ENABLED {
		t.Fatalf("expected ENABLED, got %s", v.State)
	}

	// Should be accessible again.
	resp, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: versionName,
	})
	if err != nil {
		t.Fatalf("AccessSecretVersion after enable: %v", err)
	}
	if string(resp.Payload.Data) != "data" {
		t.Fatalf("unexpected payload: %q", string(resp.Payload.Data))
	}
}

func TestDestroyVersion(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	secretName := "projects/test-project/secrets/destroy-test"
	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "destroy-test",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	_, err = client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte("data")},
	})
	if err != nil {
		t.Fatalf("AddSecretVersion: %v", err)
	}

	versionName := secretName + "/versions/1"

	// Destroy.
	v, err := client.DestroySecretVersion(ctx, &secretmanagerpb.DestroySecretVersionRequest{
		Name: versionName,
	})
	if err != nil {
		t.Fatalf("DestroySecretVersion: %v", err)
	}
	if v.State != secretmanagerpb.SecretVersion_DESTROYED {
		t.Fatalf("expected DESTROYED, got %s", v.State)
	}
	if v.DestroyTime == nil {
		t.Fatal("expected destroy_time to be set")
	}

	// Access destroyed version should fail with FailedPrecondition.
	_, err = client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: versionName,
	})
	if err == nil {
		t.Fatal("expected error accessing destroyed version")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestVersionStateTransitions(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	secretName := "projects/test-project/secrets/transitions"
	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "transitions",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	_, err = client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte("data")},
	})
	if err != nil {
		t.Fatalf("AddSecretVersion: %v", err)
	}

	versionName := secretName + "/versions/1"

	// Destroy the version.
	_, err = client.DestroySecretVersion(ctx, &secretmanagerpb.DestroySecretVersionRequest{
		Name: versionName,
	})
	if err != nil {
		t.Fatalf("DestroySecretVersion: %v", err)
	}

	// Cannot enable a destroyed version.
	_, err = client.EnableSecretVersion(ctx, &secretmanagerpb.EnableSecretVersionRequest{
		Name: versionName,
	})
	if err == nil {
		t.Fatal("expected error enabling destroyed version")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}

	// Cannot disable a destroyed version.
	_, err = client.DisableSecretVersion(ctx, &secretmanagerpb.DisableSecretVersionRequest{
		Name: versionName,
	})
	if err == nil {
		t.Fatal("expected error disabling destroyed version")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestDeleteSecretDeletesVersions(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	secretName := "projects/test-project/secrets/del-with-versions"
	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "del-with-versions",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	_, err = client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte("data")},
	})
	if err != nil {
		t.Fatalf("AddSecretVersion: %v", err)
	}

	_, err = client.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{
		Name: secretName,
	})
	if err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}

	// Accessing the version should fail.
	_, err = client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: secretName + "/versions/1",
	})
	if err == nil {
		t.Fatal("expected error after secret deletion")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestUnimplementedRPC(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	_, err := client.UpdateSecret(ctx, &secretmanagerpb.UpdateSecretRequest{})
	if err == nil {
		t.Fatal("expected error for unimplemented RPC")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %v", err)
	}
}

func TestMultipleProjects(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	// Create secrets in two different projects.
	for _, proj := range []string{"projects/proj-a", "projects/proj-b"} {
		_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
			Parent:   proj,
			SecretId: "shared-name",
			Secret:   &secretmanagerpb.Secret{},
		})
		if err != nil {
			t.Fatalf("CreateSecret in %s: %v", proj, err)
		}
	}

	// List should only return secrets for the specified project.
	resp, err := client.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{
		Parent: "projects/proj-a",
	})
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(resp.Secrets) != 1 {
		t.Fatalf("expected 1 secret in proj-a, got %d", len(resp.Secrets))
	}
}

func TestGetSecretVersion(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	secretName := "projects/test-project/secrets/get-version"
	_, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "get-version",
		Secret:   &secretmanagerpb.Secret{},
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	_, err = client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte("hello")},
	})
	if err != nil {
		t.Fatalf("AddSecretVersion: %v", err)
	}

	v, err := client.GetSecretVersion(ctx, &secretmanagerpb.GetSecretVersionRequest{
		Name: secretName + "/versions/1",
	})
	if err != nil {
		t.Fatalf("GetSecretVersion: %v", err)
	}
	if v.State != secretmanagerpb.SecretVersion_ENABLED {
		t.Fatalf("expected ENABLED, got %s", v.State)
	}

	// Get latest.
	v2, err := client.GetSecretVersion(ctx, &secretmanagerpb.GetSecretVersionRequest{
		Name: secretName + "/versions/latest",
	})
	if err != nil {
		t.Fatalf("GetSecretVersion(latest): %v", err)
	}
	if v2.Name != v.Name {
		t.Fatalf("latest should match version 1: %s vs %s", v2.Name, v.Name)
	}
}
