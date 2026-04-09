package vertexai

import (
	"bufio"
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
	Tools    []ollamaTool    `json:"tools,omitempty"`
}

type ollamaMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
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

func (o *OllamaBackend) buildMessages(req *GenerateRequest) []ollamaMessage {
	var messages []ollamaMessage

	if req.SystemInstruction != "" {
		messages = append(messages, ollamaMessage{Role: "system", Content: req.SystemInstruction})
	}

	for _, m := range req.Messages {
		role := m.Role
		if role == "model" {
			role = "assistant"
		}
		msg := ollamaMessage{Role: role, Content: m.Content}
		if m.FunctionCall != nil {
			msg.ToolCalls = []ollamaToolCall{{
				Function: ollamaFunctionCall{
					Name:      m.FunctionCall.Name,
					Arguments: m.FunctionCall.Args,
				},
			}}
		}
		if m.FunctionResponse != nil {
			msg.Role = "tool"
			respJSON, _ := json.Marshal(m.FunctionResponse.Response)
			msg.Content = string(respJSON)
		}
		messages = append(messages, msg)
	}
	return messages
}

func (o *OllamaBackend) buildTools(tools []Tool) []ollamaTool {
	var result []ollamaTool
	for _, t := range tools {
		result = append(result, ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return result
}

func (o *OllamaBackend) buildRequest(model string, req *GenerateRequest, stream bool) ollamaChatRequest {
	ollamaReq := ollamaChatRequest{
		Model:    model,
		Messages: o.buildMessages(req),
		Stream:   stream,
	}

	if req.Temperature != nil || req.MaxOutputTokens != nil {
		ollamaReq.Options = &ollamaOptions{
			Temperature: req.Temperature,
			NumPredict:  req.MaxOutputTokens,
		}
	}

	if len(req.Tools) > 0 {
		ollamaReq.Tools = o.buildTools(req.Tools)
	}

	return ollamaReq
}

func (o *OllamaBackend) parseToolCalls(msg ollamaMessage) *FunctionCall {
	if len(msg.ToolCalls) > 0 {
		tc := msg.ToolCalls[0]
		return &FunctionCall{
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		}
	}
	return nil
}

func (o *OllamaBackend) GenerateContent(model string, req *GenerateRequest) (*GenerateResponse, error) {
	ollamaReq := o.buildRequest(model, req, false)

	body, _ := json.Marshal(ollamaReq)
	resp, err := o.client.Post(o.host+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
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

	genResp := &GenerateResponse{
		Text:         chatResp.Message.Content,
		FinishReason: "STOP",
		FunctionCall: o.parseToolCalls(chatResp.Message),
	}
	return genResp, nil
}

func (o *OllamaBackend) StreamGenerateContent(model string, req *GenerateRequest, ch chan<- StreamChunk) error {
	defer close(ch)

	ollamaReq := o.buildRequest(model, req, true)

	body, _ := json.Marshal(ollamaReq)
	resp, err := o.client.Post(o.host+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		// Ollama unreachable, fall back to stub streaming.
		stubCh := make(chan StreamChunk, 64)
		go o.stub.StreamGenerateContent(model, req, stubCh)
		for chunk := range stubCh {
			ch <- chunk
		}
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("model %q not found in Ollama", model)
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("Ollama error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Ollama streams NDJSON: one JSON object per line.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var chatResp ollamaChatResponse
		if err := json.Unmarshal(scanner.Bytes(), &chatResp); err != nil {
			continue
		}
		chunk := StreamChunk{
			Text: chatResp.Message.Content,
			Done: chatResp.Done,
		}
		if chatResp.Done {
			chunk.FinishReason = "STOP"
			chunk.FunctionCall = o.parseToolCalls(chatResp.Message)
		}
		ch <- chunk
	}
	return scanner.Err()
}

func (o *OllamaBackend) EmbedContent(model string, req *EmbedRequest) (*EmbedResponse, error) {
	ollamaReq := ollamaEmbedRequest{
		Model: model,
		Input: req.Text,
	}

	body, _ := json.Marshal(ollamaReq)
	resp, err := o.client.Post(o.host+"/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
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
