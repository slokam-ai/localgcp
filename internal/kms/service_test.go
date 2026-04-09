package kms

import (
	"context"
	"crypto/sha256"
	"net"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func testClient(t *testing.T) (kmspb.KeyManagementServiceClient, func()) {
	t.Helper()
	svc := New("", true)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	kmspb.RegisterKeyManagementServiceServer(srv, svc)
	go srv.Serve(ln)

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatalf("dial: %v", err)
	}
	client := kmspb.NewKeyManagementServiceClient(conn)
	return client, func() { conn.Close(); srv.Stop() }
}

func TestKeyRingCRUD(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	parent := "projects/test/locations/global"

	// Create.
	kr, err := client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
		Parent:    parent,
		KeyRingId: "my-ring",
	})
	if err != nil {
		t.Fatalf("CreateKeyRing: %v", err)
	}
	if kr.Name != parent+"/keyRings/my-ring" {
		t.Fatalf("wrong name: %s", kr.Name)
	}

	// Get.
	kr2, err := client.GetKeyRing(ctx, &kmspb.GetKeyRingRequest{Name: kr.Name})
	if err != nil {
		t.Fatalf("GetKeyRing: %v", err)
	}
	if kr2.Name != kr.Name {
		t.Fatalf("get returned wrong name")
	}

	// List.
	resp, err := client.ListKeyRings(ctx, &kmspb.ListKeyRingsRequest{Parent: parent})
	if err != nil {
		t.Fatalf("ListKeyRings: %v", err)
	}
	if len(resp.KeyRings) != 1 {
		t.Fatalf("expected 1 key ring, got %d", len(resp.KeyRings))
	}
}

func TestSymmetricEncryptDecrypt(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	parent := "projects/test/locations/global"
	client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{Parent: parent, KeyRingId: "ring1"})

	// Create symmetric key.
	ck, err := client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      parent + "/keyRings/ring1",
		CryptoKeyId: "sym-key",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
		},
	})
	if err != nil {
		t.Fatalf("CreateCryptoKey: %v", err)
	}

	plaintext := []byte("hello world, this is a secret message")

	// Encrypt.
	encResp, err := client.Encrypt(ctx, &kmspb.EncryptRequest{
		Name:      ck.Name,
		Plaintext: plaintext,
	})
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Decrypt.
	decResp, err := client.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:       ck.Name,
		Ciphertext: encResp.Ciphertext,
	})
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if string(decResp.Plaintext) != string(plaintext) {
		t.Fatalf("round trip failed: got %q", string(decResp.Plaintext))
	}
}

func TestAsymmetricSignAndGetPublicKey(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	parent := "projects/test/locations/global"
	client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{Parent: parent, KeyRingId: "ring2"})

	// Create asymmetric signing key.
	ck, err := client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      parent + "/keyRings/ring2",
		CryptoKeyId: "sign-key",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ASYMMETRIC_SIGN,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateCryptoKey: %v", err)
	}

	versionName := ck.Primary.Name

	// Get public key.
	pubKey, err := client.GetPublicKey(ctx, &kmspb.GetPublicKeyRequest{Name: versionName})
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	if pubKey.Pem == "" {
		t.Fatal("expected PEM public key")
	}
	if pubKey.Algorithm != kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256 {
		t.Fatalf("wrong algorithm: %v", pubKey.Algorithm)
	}

	// Sign.
	data := []byte("data to sign")
	digest := sha256.Sum256(data)
	sigResp, err := client.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
		Name: versionName,
		Digest: &kmspb.Digest{
			Digest: &kmspb.Digest_Sha256{Sha256: digest[:]},
		},
	})
	if err != nil {
		t.Fatalf("AsymmetricSign: %v", err)
	}
	if len(sigResp.Signature) == 0 {
		t.Fatal("expected non-empty signature")
	}
}

func TestMacSignVerify(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	parent := "projects/test/locations/global"
	client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{Parent: parent, KeyRingId: "ring3"})

	ck, err := client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      parent + "/keyRings/ring3",
		CryptoKeyId: "mac-key",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_MAC,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_HMAC_SHA256,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateCryptoKey: %v", err)
	}

	versionName := ck.Primary.Name
	data := []byte("message to mac")

	// Sign.
	macResp, err := client.MacSign(ctx, &kmspb.MacSignRequest{Name: versionName, Data: data})
	if err != nil {
		t.Fatalf("MacSign: %v", err)
	}

	// Verify (correct).
	verifyResp, err := client.MacVerify(ctx, &kmspb.MacVerifyRequest{Name: versionName, Data: data, Mac: macResp.Mac})
	if err != nil {
		t.Fatalf("MacVerify: %v", err)
	}
	if !verifyResp.Success {
		t.Fatal("expected MAC verification to succeed")
	}

	// Verify (wrong data).
	verifyResp, err = client.MacVerify(ctx, &kmspb.MacVerifyRequest{Name: versionName, Data: []byte("wrong"), Mac: macResp.Mac})
	if err != nil {
		t.Fatalf("MacVerify: %v", err)
	}
	if verifyResp.Success {
		t.Fatal("expected MAC verification to fail for wrong data")
	}
}

func TestDestroyVersion(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	parent := "projects/test/locations/global"
	client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{Parent: parent, KeyRingId: "ring4"})

	ck, err := client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      parent + "/keyRings/ring4",
		CryptoKeyId: "doomed-key",
		CryptoKey:   &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT},
	})
	if err != nil {
		t.Fatalf("CreateCryptoKey: %v", err)
	}

	// Destroy.
	v, err := client.DestroyCryptoKeyVersion(ctx, &kmspb.DestroyCryptoKeyVersionRequest{Name: ck.Primary.Name})
	if err != nil {
		t.Fatalf("DestroyCryptoKeyVersion: %v", err)
	}
	if v.State != kmspb.CryptoKeyVersion_DESTROYED {
		t.Fatalf("expected DESTROYED, got %v", v.State)
	}

	// Encrypt should fail on destroyed key.
	_, err = client.Encrypt(ctx, &kmspb.EncryptRequest{Name: ck.Name, Plaintext: []byte("test")})
	if err == nil {
		t.Fatal("expected error encrypting with destroyed version")
	}
}
