package vertexai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaBackend translates Vertex AI requests to Ollama API calls.
type OllamaBackend struct {
	host   string
	client *http.Client
	// Falls back to stub if Ollama is unreachable.
	stub *StubBackend
}

func NewOllamaBackend(host string) *OllamaBackend {
	return &OllamaBackend{
		host:   host,
		client: &http.Client{Timeout: 120 * time.Second},
		stub:   &StubBackend{},
	}
}

// ollamaChatRequest matches Ollama's /api/chat request format.
type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  *ollamaOptions  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int32   `json:"num_predict,omitempty"`
}

// ollamaChatResponse matches Ollama's /api/chat response format.
type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
}

// ollamaEmbedRequest matches Ollama's /api/embed request format.
type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

func (o *OllamaBackend) GenerateContent(model string, req *GenerateRequest) (*GenerateResponse, error) {
	// Build Ollama messages.
	var messages []ollamaMessage

	// System instruction goes first.
	if req.SystemInstruction != "" {
		messages = append(messages, ollamaMessage{Role: "system", Content: req.SystemInstruction})
	}

	for _, m := range req.Messages {
		role := m.Role
		if role == "model" {
			role = "assistant"
		}
		messages = append(messages, ollamaMessage{Role: role, Content: m.Content})
	}

	ollamaReq := ollamaChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
	}

	if req.Temperature != nil || req.MaxOutputTokens != nil {
		ollamaReq.Options = &ollamaOptions{
			Temperature: req.Temperature,
			NumPredict:  req.MaxOutputTokens,
		}
	}

	body, _ := json.Marshal(ollamaReq)
	resp, err := o.client.Post(o.host+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		// Ollama unreachable, fall back to stub.
		return o.stub.GenerateContent(model, req)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("model %q not found in Ollama", model)
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("Ollama error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode Ollama response: %w", err)
	}

	return &GenerateResponse{
		Text:         chatResp.Message.Content,
		FinishReason: "STOP",
	}, nil
}

func (o *OllamaBackend) EmbedContent(model string, req *EmbedRequest) (*EmbedResponse, error) {
	ollamaReq := ollamaEmbedRequest{
		Model: model,
		Input: req.Text,
	}

	body, _ := json.Marshal(ollamaReq)
	resp, err := o.client.Post(o.host+"/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		// Ollama unreachable, fall back to stub.
		return o.stub.EmbedContent(model, req)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("Ollama embed error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var embedResp ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("decode Ollama embed response: %w", err)
	}

	if len(embedResp.Embeddings) == 0 {
		return nil, fmt.Errorf("Ollama returned no embeddings")
	}

	return &EmbedResponse{Values: embedResp.Embeddings[0]}, nil
}
