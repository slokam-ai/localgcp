package vertexai

// GenerateRequest is the intermediate representation of a Vertex AI generateContent request.
type GenerateRequest struct {
	Model             string
	Messages          []Message
	SystemInstruction string
	Temperature       *float64
	MaxOutputTokens   *int32
	Tools             []Tool // tool/function declarations
}

// Message represents a single turn in a conversation.
type Message struct {
	Role         string        // "user" or "model"
	Content      string        // text content (may be empty if FunctionCall or FunctionResponse is set)
	FunctionCall *FunctionCall // non-nil when the model invokes a tool
	FunctionResponse *FunctionResponse // non-nil when the user returns a tool result
}

// Tool declares a function the model may call.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]interface{} // JSON Schema
}

// FunctionCall represents the model requesting to call a function.
type FunctionCall struct {
	Name string
	Args map[string]interface{}
}

// FunctionResponse represents the user returning a function result.
type FunctionResponse struct {
	Name     string
	Response map[string]interface{}
}

// GenerateResponse is the intermediate representation of a generateContent response.
type GenerateResponse struct {
	Text         string
	FinishReason string        // "STOP", "MAX_TOKENS", etc.
	FunctionCall *FunctionCall // non-nil when the model wants to call a tool
}

// StreamChunk is a single chunk in a streaming response.
type StreamChunk struct {
	Text         string
	FinishReason string        // empty until the last chunk
	FunctionCall *FunctionCall // non-nil only in the final chunk if tool call
	Done         bool
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
	StreamGenerateContent(model string, req *GenerateRequest, ch chan<- StreamChunk) error
	EmbedContent(model string, req *EmbedRequest) (*EmbedResponse, error)
}
