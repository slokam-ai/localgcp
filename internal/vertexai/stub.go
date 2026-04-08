package vertexai

import (
	"crypto/sha256"
	"fmt"
)

// StubBackend returns deterministic responses without any external model.
type StubBackend struct{}

func (s *StubBackend) GenerateContent(model string, req *GenerateRequest) (*GenerateResponse, error) {
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

func (s *StubBackend) EmbedContent(model string, req *EmbedRequest) (*EmbedResponse, error) {
	// Generate a deterministic 768-dimension vector from SHA-256 of input.
	hash := sha256.Sum256([]byte(req.Text))
	values := make([]float64, 768)
	for i := 0; i < 768; i++ {
		values[i] = float64(hash[i%32]) / 255.0
	}
	return &EmbedResponse{Values: values}, nil
}
