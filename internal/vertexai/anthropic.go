package vertexai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AnthropicBackend translates Vertex AI requests to Anthropic Messages API calls.
type AnthropicBackend struct {
	apiKey string
	host   string
	client *http.Client
}

func NewAnthropicBackend(apiKey, host string) *AnthropicBackend {
	if host == "" {
		host = "https://api.anthropic.com"
	}
	return &AnthropicBackend{
		apiKey: apiKey,
		host:   strings.TrimRight(host, "/"),
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// --- Anthropic request/response types ---

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int32              `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string               `json:"role"`
	Content json.RawMessage      `json:"content"` // string or []contentBlock
}

type anthropicContentBlock struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text,omitempty"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   string                 `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
}

// Streaming event types
type anthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Delta        *anthropicStreamDelta  `json:"delta,omitempty"`
	ContentBlock *anthropicContentBlock `json:"content_block,omitempty"`
}

type anthropicStreamDelta struct {
	Type         string                 `json:"type,omitempty"`
	Text         string                 `json:"text,omitempty"`
	StopReason   string                 `json:"stop_reason,omitempty"`
	PartialJSON  string                 `json:"partial_json,omitempty"`
}

func (a *AnthropicBackend) buildRequest(model string, req *GenerateRequest, stream bool) anthropicRequest {
	var messages []anthropicMessage

	for _, m := range req.Messages {
		role := m.Role
		if role == "model" {
			role = "assistant"
		}

		var blocks []anthropicContentBlock

		if m.Content != "" {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
		}
		if m.FunctionCall != nil {
			blocks = append(blocks, anthropicContentBlock{
				Type:  "tool_use",
				ID:    "toolu_" + m.FunctionCall.Name,
				Name:  m.FunctionCall.Name,
				Input: m.FunctionCall.Args,
			})
		}
		if m.FunctionResponse != nil {
			role = "user"
			respJSON, _ := json.Marshal(m.FunctionResponse.Response)
			blocks = append(blocks, anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: "toolu_" + m.FunctionResponse.Name,
				Content:   string(respJSON),
			})
		}

		content, _ := json.Marshal(blocks)
		messages = append(messages, anthropicMessage{
			Role:    role,
			Content: content,
		})
	}

	maxTokens := int32(4096)
	if req.MaxOutputTokens != nil {
		maxTokens = *req.MaxOutputTokens
	}

	antReq := anthropicRequest{
		Model:       model,
		Messages:    messages,
		System:      req.SystemInstruction,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		Stream:      stream,
	}

	for _, t := range req.Tools {
		antReq.Tools = append(antReq.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	return antReq
}

func (a *AnthropicBackend) doRequest(body []byte) (*http.Response, error) {
	httpReq, _ := http.NewRequest("POST", a.host+"/v1/messages", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	return a.client.Do(httpReq)
}

func (a *AnthropicBackend) GenerateContent(model string, req *GenerateRequest) (*GenerateResponse, error) {
	antReq := a.buildRequest(model, req, false)
	body, _ := json.Marshal(antReq)

	resp, err := a.doRequest(body)
	if err != nil {
		return nil, fmt.Errorf("Anthropic request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("Anthropic error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var antResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&antResp); err != nil {
		return nil, fmt.Errorf("decode Anthropic response: %w", err)
	}

	genResp := &GenerateResponse{
		FinishReason: mapAnthropicStopReason(antResp.StopReason),
	}

	for _, block := range antResp.Content {
		switch block.Type {
		case "text":
			genResp.Text += block.Text
		case "tool_use":
			genResp.FunctionCall = &FunctionCall{
				Name: block.Name,
				Args: block.Input,
			}
		}
	}

	return genResp, nil
}

func (a *AnthropicBackend) StreamGenerateContent(model string, req *GenerateRequest, ch chan<- StreamChunk) error {
	defer close(ch)

	antReq := a.buildRequest(model, req, true)
	body, _ := json.Marshal(antReq)

	resp, err := a.doRequest(body)
	if err != nil {
		return fmt.Errorf("Anthropic stream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("Anthropic error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Anthropic streams SSE: "event: type\ndata: {json}\n\n"
	scanner := bufio.NewScanner(resp.Body)
	var currentToolCall *FunctionCall
	var toolInputJSON string

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var evt anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}

		switch evt.Type {
		case "content_block_start":
			if evt.ContentBlock != nil && evt.ContentBlock.Type == "tool_use" {
				currentToolCall = &FunctionCall{Name: evt.ContentBlock.Name}
				toolInputJSON = ""
			}
		case "content_block_delta":
			if evt.Delta == nil {
				continue
			}
			if evt.Delta.Type == "text_delta" {
				ch <- StreamChunk{Text: evt.Delta.Text}
			} else if evt.Delta.Type == "input_json_delta" && currentToolCall != nil {
				toolInputJSON += evt.Delta.PartialJSON
			}
		case "content_block_stop":
			if currentToolCall != nil {
				var args map[string]interface{}
				json.Unmarshal([]byte(toolInputJSON), &args)
				currentToolCall.Args = args
			}
		case "message_delta":
			if evt.Delta != nil && evt.Delta.StopReason != "" {
				chunk := StreamChunk{
					FinishReason: mapAnthropicStopReason(evt.Delta.StopReason),
					Done:         true,
				}
				if currentToolCall != nil {
					chunk.FunctionCall = currentToolCall
					currentToolCall = nil
				}
				ch <- chunk
			}
		}
	}
	return scanner.Err()
}

func (a *AnthropicBackend) EmbedContent(_ string, _ *EmbedRequest) (*EmbedResponse, error) {
	return nil, fmt.Errorf("Anthropic does not support embeddings; use Ollama or OpenAI backend for embedding models")
}

func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "STOP"
	case "max_tokens":
		return "MAX_TOKENS"
	case "tool_use":
		return "STOP"
	default:
		return "STOP"
	}
}
