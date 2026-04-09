package vertexai

import (
	"context"
	"encoding/json"
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

	svc := New("", true, "", "", "", "")
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
	svc := New("", true, "", "custom-model=my-ollama-model", "", "")
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

func TestHTTPStreamGenerateContent(t *testing.T) {
	base, cleanup := testServer(t)
	defer cleanup()

	body := `{"contents":[{"role":"user","parts":[{"text":"stream test"}]}]}`
	resp, err := http.Post(
		base+"/v1beta1/projects/test/locations/us/publishers/google/models/gemini-2.5-flash:streamGenerateContent",
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

	// Response should be a JSON array of chunks.
	var chunks []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&chunks); err != nil {
		t.Fatalf("decode stream response: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	// Reconstruct the full text from all chunks.
	var fullText string
	for _, chunk := range chunks {
		candidates, _ := chunk["candidates"].([]interface{})
		if len(candidates) == 0 {
			continue
		}
		cand, _ := candidates[0].(map[string]interface{})
		content, _ := cand["content"].(map[string]interface{})
		parts, _ := content["parts"].([]interface{})
		for _, p := range parts {
			part, _ := p.(map[string]interface{})
			if text, ok := part["text"].(string); ok {
				fullText += text
			}
		}
	}
	if !strings.Contains(fullText, "localgcp stub") {
		t.Fatalf("expected stub marker in streamed response, got: %s", fullText)
	}
}

func TestHTTPFunctionCalling(t *testing.T) {
	base, cleanup := testServer(t)
	defer cleanup()

	// Request with tool declarations — stub should respond with a function call.
	body := `{
		"contents":[{"role":"user","parts":[{"text":"what is the weather?"}]}],
		"tools":[{
			"functionDeclarations":[{
				"name":"get_weather",
				"description":"Get current weather",
				"parameters":{"type":"object","properties":{"location":{"type":"string"}}}
			}]
		}]
	}`
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

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	candidates, _ := result["candidates"].([]interface{})
	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}
	cand, _ := candidates[0].(map[string]interface{})
	content, _ := cand["content"].(map[string]interface{})
	parts, _ := content["parts"].([]interface{})
	if len(parts) == 0 {
		t.Fatal("expected parts")
	}
	part, _ := parts[0].(map[string]interface{})
	fc, ok := part["functionCall"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected functionCall in response, got: %v", part)
	}
	if fc["name"] != "get_weather" {
		t.Fatalf("expected function name get_weather, got: %v", fc["name"])
	}
}

func TestHTTPStreamFunctionCalling(t *testing.T) {
	base, cleanup := testServer(t)
	defer cleanup()

	body := `{
		"contents":[{"role":"user","parts":[{"text":"call a tool"}]}],
		"tools":[{
			"functionDeclarations":[{
				"name":"lookup",
				"description":"Look up data",
				"parameters":{"type":"object","properties":{"query":{"type":"string"}}}
			}]
		}]
	}`
	resp, err := http.Post(
		base+"/v1beta1/projects/test/locations/us/publishers/google/models/gemini-2.5-flash:streamGenerateContent",
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

	var chunks []map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&chunks)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	// The last chunk should have a function call.
	last := chunks[len(chunks)-1]
	candidates, _ := last["candidates"].([]interface{})
	if len(candidates) == 0 {
		t.Fatal("expected candidates in last chunk")
	}
	cand, _ := candidates[0].(map[string]interface{})
	content, _ := cand["content"].(map[string]interface{})
	parts, _ := content["parts"].([]interface{})
	if len(parts) == 0 {
		t.Fatal("expected parts in last chunk")
	}
	part, _ := parts[0].(map[string]interface{})
	if _, ok := part["functionCall"]; !ok {
		t.Fatalf("expected functionCall in last chunk, got: %v", part)
	}
}

func TestFunctionResponseRoundTrip(t *testing.T) {
	base, cleanup := testServer(t)
	defer cleanup()

	// Send a conversation with a function response — stub should echo it back as text.
	body := `{
		"contents":[
			{"role":"user","parts":[{"text":"what is the weather?"}]},
			{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"location":"NYC"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"temp":"72F"}}}]}
		]
	}`
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

func TestMultiProviderBackendSelection(t *testing.T) {
	// Stub backend.
	svc := New("", true, "", "", "stub", "")
	if _, ok := svc.backend.(*StubBackend); !ok {
		t.Fatal("expected StubBackend")
	}

	// OpenAI backend.
	svc = New("", true, "", "", "openai", "test-key")
	if _, ok := svc.backend.(*OpenAIBackend); !ok {
		t.Fatal("expected OpenAIBackend")
	}

	// Anthropic backend.
	svc = New("", true, "", "", "anthropic", "test-key")
	if _, ok := svc.backend.(*AnthropicBackend); !ok {
		t.Fatal("expected AnthropicBackend")
	}

	// Default (ollama with no host) falls back to stub.
	svc = New("", true, "", "", "", "")
	if _, ok := svc.backend.(*StubBackend); !ok {
		t.Fatal("expected StubBackend when no ollama host")
	}

	// Default with ollama host.
	svc = New("", true, "http://localhost:11434", "", "", "")
	if _, ok := svc.backend.(*OllamaBackend); !ok {
		t.Fatal("expected OllamaBackend")
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
