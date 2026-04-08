package vertexai

// GenerateRequest is the intermediate representation of a Vertex AI generateContent request.
type GenerateRequest struct {
	Model            string
	Messages         []Message
	SystemInstruction string
	Temperature      *float64
	MaxOutputTokens  *int32
}

// Message represents a single turn in a conversation.
type Message struct {
	Role    string // "user" or "model"
	Content string
}

// GenerateResponse is the intermediate representation of a generateContent response.
type GenerateResponse struct {
	Text         string
	FinishReason string // "STOP", "MAX_TOKENS", etc.
}

// EmbedRequest is the intermediate representation of an embedContent request.
type EmbedRequest struct {
	Model string
	Text  string
}

// EmbedResponse is the intermediate representation of an embedContent response.
type EmbedResponse struct {
	Values []float64
}

// Backend is the interface for AI inference providers (Ollama, stub, etc).
type Backend interface {
	GenerateContent(model string, req *GenerateRequest) (*GenerateResponse, error)
	EmbedContent(model string, req *EmbedRequest) (*EmbedResponse, error)
}
