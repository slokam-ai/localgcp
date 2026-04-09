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

// OpenAIBackend translates Vertex AI requests to OpenAI API calls.
type OpenAIBackend struct {
	apiKey string
	host   string // base URL, e.g. "https://api.openai.com"
	client *http.Client
}

func NewOpenAIBackend(apiKey, host string) *OpenAIBackend {
	if host == "" {
		host = "https://api.openai.com"
	}
	return &OpenAIBackend{
		apiKey: apiKey,
		host:   strings.TrimRight(host, "/"),
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// --- OpenAI request/response types ---

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int32          `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream"`
	Tools       []openAITool    `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type openAIToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function openAIToolCallFunc   `json:"function"`
}

type openAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
}

type openAIChoice struct {
	Message      openAIMessage `json:"message"`
	Delta        openAIMessage `json:"delta"`
	FinishReason *string       `json:"finish_reason"`
}

func (o *OpenAIBackend) buildRequest(model string, req *GenerateRequest, stream bool) openAIRequest {
	var messages []openAIMessage

	if req.SystemInstruction != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: req.SystemInstruction})
	}

	for _, m := range req.Messages {
		role := m.Role
		if role == "model" {
			role = "assistant"
		}
		msg := openAIMessage{Role: role, Content: m.Content}
		if m.FunctionCall != nil {
			argsJSON, _ := json.Marshal(m.FunctionCall.Args)
			msg.ToolCalls = []openAIToolCall{{
				ID:   "call_" + m.FunctionCall.Name,
				Type: "function",
				Function: openAIToolCallFunc{
					Name:      m.FunctionCall.Name,
					Arguments: string(argsJSON),
				},
			}}
		}
		if m.FunctionResponse != nil {
			msg.Role = "tool"
			msg.ToolCallID = "call_" + m.FunctionResponse.Name
			respJSON, _ := json.Marshal(m.FunctionResponse.Response)
			msg.Content = string(respJSON)
		}
		messages = append(messages, msg)
	}

	oaiReq := openAIRequest{
		Model:       model,
		Messages:    messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxOutputTokens,
		Stream:      stream,
	}

	for _, t := range req.Tools {
		oaiReq.Tools = append(oaiReq.Tools, openAITool{
			Type: "function",
			Function: openAIToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	return oaiReq
}

func (o *OpenAIBackend) doRequest(body []byte) (*http.Response, error) {
	httpReq, _ := http.NewRequest("POST", o.host+"/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	return o.client.Do(httpReq)
}

func parseOpenAIToolCalls(msg openAIMessage) *FunctionCall {
	if len(msg.ToolCalls) > 0 {
		tc := msg.ToolCalls[0]
		var args map[string]interface{}
		json.Unmarshal([]byte(tc.Function.Arguments), &args)
		return &FunctionCall{
			Name: tc.Function.Name,
			Args: args,
		}
	}
	return nil
}

func (o *OpenAIBackend) GenerateContent(model string, req *GenerateRequest) (*GenerateResponse, error) {
	oaiReq := o.buildRequest(model, req, false)
	body, _ := json.Marshal(oaiReq)

	resp, err := o.doRequest(body)
	if err != nil {
		return nil, fmt.Errorf("OpenAI request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("OpenAI error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("decode OpenAI response: %w", err)
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("OpenAI returned no choices")
	}

	choice := oaiResp.Choices[0]
	return &GenerateResponse{
		Text:         choice.Message.Content,
		FinishReason: mapOpenAIFinishReason(choice.FinishReason),
		FunctionCall: parseOpenAIToolCalls(choice.Message),
	}, nil
}

func (o *OpenAIBackend) StreamGenerateContent(model string, req *GenerateRequest, ch chan<- StreamChunk) error {
	defer close(ch)

	oaiReq := o.buildRequest(model, req, true)
	body, _ := json.Marshal(oaiReq)

	resp, err := o.doRequest(body)
	if err != nil {
		return fmt.Errorf("OpenAI stream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("OpenAI error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// OpenAI streams SSE: "data: {json}\n\n" lines.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var oaiResp openAIResponse
		if err := json.Unmarshal([]byte(data), &oaiResp); err != nil {
			continue
		}
		if len(oaiResp.Choices) == 0 {
			continue
		}

		choice := oaiResp.Choices[0]
		chunk := StreamChunk{
			Text: choice.Delta.Content,
		}
		if choice.FinishReason != nil {
			chunk.FinishReason = mapOpenAIFinishReason(choice.FinishReason)
			chunk.Done = true
			chunk.FunctionCall = parseOpenAIToolCalls(choice.Delta)
		}
		ch <- chunk
	}
	return scanner.Err()
}

func (o *OpenAIBackend) EmbedContent(model string, req *EmbedRequest) (*EmbedResponse, error) {
	embReq := map[string]interface{}{
		"model": model,
		"input": req.Text,
	}
	body, _ := json.Marshal(embReq)

	httpReq, _ := http.NewRequest("POST", o.host+"/v1/embeddings", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("OpenAI embed request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("OpenAI embed error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var embResp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode OpenAI embed response: %w", err)
	}
	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("OpenAI returned no embeddings")
	}

	return &EmbedResponse{Values: embResp.Data[0].Embedding}, nil
}

func mapOpenAIFinishReason(reason *string) string {
	if reason == nil {
		return ""
	}
	switch *reason {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "tool_calls":
		return "STOP"
	default:
		return "STOP"
	}
}
