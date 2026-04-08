package vertexai

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// testServer starts a Vertex AI service on an ephemeral port with stub backend.
func testServer(t *testing.T) (string, func()) {
	t.Helper()

	svc := New("", true, "", "")
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	addr := fmt.Sprintf(":%d", port)
	ctx, cancel := context.WithCancel(context.Background())
	go svc.Start(ctx, addr)

	base := fmt.Sprintf("http://localhost:%d", port)
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return base, cancel
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatal("server did not start")
	return "", nil
}

// TestSDKGenerateContent is the thesis validation: google.golang.org/genai talks to localhost.
// Requires GCP default credentials (the SDK checks even with custom BaseURL).
// Skipped in CI where no credentials exist.
func TestSDKGenerateContent(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("skipping SDK test in CI: no GCP credentials")
	}
	base, cleanup := testServer(t)
	defer cleanup()

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Project:  "test-project",
		Location: "us-central1",
		Backend:  genai.BackendVertexAI,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: base,
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.Models.GenerateContent(context.Background(), "gemini-2.5-flash",
		genai.Text("Hello from the SDK test"),
		nil,
	)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	if len(resp.Candidates) == 0 {
		t.Fatal("expected at least one candidate")
	}
	text := resp.Text()
	if text == "" {
		t.Fatal("expected non-empty response text")
	}
	if !strings.Contains(text, "localgcp stub") {
		t.Fatalf("expected stub marker in response, got: %s", text)
	}
	t.Logf("SDK response: %s", text)
}

func TestSDKEmbedContent(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("skipping SDK test in CI: no GCP credentials")
	}
	base, cleanup := testServer(t)
	defer cleanup()

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Project:  "test-project",
		Location: "us-central1",
		Backend:  genai.BackendVertexAI,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: base,
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.Models.EmbedContent(context.Background(), "text-embedding-004",
		genai.Text("test embedding input"),
		nil,
	)
	if err != nil {
		t.Fatalf("EmbedContent: %v", err)
	}

	if len(resp.Embeddings) == 0 {
		t.Fatal("expected embeddings in response")
	}
	if len(resp.Embeddings[0].Values) != 768 {
		t.Fatalf("expected 768-dim embedding, got %d", len(resp.Embeddings[0].Values))
	}
	t.Logf("Embedding dimension: %d", len(resp.Embeddings[0].Values))
}

func TestHTTPGenerateContent(t *testing.T) {
	base, cleanup := testServer(t)
	defer cleanup()

	body := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
	resp, err := http.Post(
		base+"/v1beta1/projects/test/locations/us/publishers/google/models/gemini-2.5-flash:generateContent",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHTTPEmbedContent(t *testing.T) {
	base, cleanup := testServer(t)
	defer cleanup()

	body := `{"content":{"parts":[{"text":"embed this"}]}}`
	resp, err := http.Post(
		base+"/v1beta1/projects/test/locations/us/publishers/google/models/text-embedding-004:embedContent",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestInvalidPath(t *testing.T) {
	base, cleanup := testServer(t)
	defer cleanup()

	resp, err := http.Post(base+"/v1beta1/invalid/path", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestMalformedJSON(t *testing.T) {
	base, cleanup := testServer(t)
	defer cleanup()

	resp, err := http.Post(
		base+"/v1beta1/projects/test/locations/us/publishers/google/models/gemini:generateContent",
		"application/json",
		strings.NewReader(`{invalid json}`),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestModelAliasResolution(t *testing.T) {
	svc := New("", true, "", "custom-model=my-ollama-model")
	if r := svc.resolveModel("custom-model"); r != "my-ollama-model" {
		t.Fatalf("expected my-ollama-model, got %s", r)
	}
	if r := svc.resolveModel("gemini-2.5-flash"); r != "llama3.2" {
		t.Fatalf("expected llama3.2, got %s", r)
	}
	if r := svc.resolveModel("unknown"); r != "unknown" {
		t.Fatalf("expected passthrough, got %s", r)
	}
}

func TestEmptyEmbedInput(t *testing.T) {
	base, cleanup := testServer(t)
	defer cleanup()

	body := `{"content":{"parts":[{"text":""}]}}`
	resp, err := http.Post(
		base+"/v1beta1/projects/test/locations/us/publishers/google/models/text-embedding-004:embedContent",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for empty input, got %d", resp.StatusCode)
	}
}
