package vertexai

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// StubBackend returns deterministic responses without any external model.
type StubBackend struct{}

func (s *StubBackend) GenerateContent(model string, req *GenerateRequest) (*GenerateResponse, error) {
	// If tools are declared and the last message looks like a question,
	// simulate a function call using the first declared tool.
	if len(req.Tools) > 0 && len(req.Messages) > 0 {
		last := req.Messages[len(req.Messages)-1]
		if last.FunctionResponse == nil {
			tool := req.Tools[0]
			return &GenerateResponse{
				FinishReason: "STOP",
				FunctionCall: &FunctionCall{
					Name: tool.Name,
					Args: map[string]interface{}{"input": last.Content},
				},
			}, nil
		}
	}

	// Build a deterministic response that includes the model name for debugging.
	input := ""
	for _, m := range req.Messages {
		input += m.Content + " "
	}
	return &GenerateResponse{
		Text:         fmt.Sprintf("[localgcp stub] echo from model %q: %s", model, input),
		FinishReason: "STOP",
	}, nil
}

func (s *StubBackend) StreamGenerateContent(model string, req *GenerateRequest, ch chan<- StreamChunk) error {
	defer close(ch)

	// Generate the full response, then split into word-level chunks.
	resp, err := s.GenerateContent(model, req)
	if err != nil {
		return err
	}

	// If it's a function call, send as a single chunk.
	if resp.FunctionCall != nil {
		ch <- StreamChunk{
			FunctionCall: resp.FunctionCall,
			FinishReason: resp.FinishReason,
			Done:         true,
		}
		return nil
	}

	words := strings.Fields(resp.Text)
	for i, word := range words {
		last := i == len(words)-1
		text := word
		if !last {
			text += " "
		}
		chunk := StreamChunk{Text: text}
		if last {
			chunk.FinishReason = resp.FinishReason
			chunk.Done = true
		}
		ch <- chunk
	}
	return nil
}

func (s *StubBackend) EmbedContent(model string, req *EmbedRequest) (*EmbedResponse, error) {
	// Generate a deterministic 768-dimension vector from SHA-256 of input.
	hash := sha256.Sum256([]byte(req.Text))
	values := make([]float64, 768)
	for i := 0; i < 768; i++ {
		values[i] = float64(hash[i%32]) / 255.0
	}
	return &EmbedResponse{Values: values}, nil
}
