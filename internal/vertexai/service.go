// Package vertexai implements a Vertex AI Gemini API emulator that proxies
// requests to local model runners (Ollama) or returns stub responses.
//
//	SDK (google.golang.org/genai)       localgcp (:8090)           Ollama (:11434)
//	  |                                      |                         |
//	  |-- POST .../models/gemini:           |                         |
//	  |   generateContent ----------------->|                         |
//	  |                                      |-- resolve model alias   |
//	  |                                      |-- translate request     |
//	  |                                      |-- POST /api/chat ------>|
//	  |                                      |<--- response -----------|
//	  |<-- GenerateContentResponse ----------|                         |
package vertexai

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Service implements the Vertex AI emulator.
type Service struct {
	dataDir    string
	quiet      bool
	logger     *log.Logger
	backend    Backend
	modelMap   map[string]string // Vertex model name -> backend model name
}

// New creates a new Vertex AI service.
func New(dataDir string, quiet bool, ollamaHost string, modelMapStr string) *Service {
	logger := log.New(os.Stderr, "[vertexai] ", log.LstdFlags)

	var backend Backend
	if ollamaHost != "" {
		backend = NewOllamaBackend(ollamaHost)
		logger.Printf("Using Ollama backend at %s", ollamaHost)
	} else {
		backend = &StubBackend{}
		logger.Printf("No Ollama host configured, using stub backend")
	}

	modelMap := defaultModelMap()
	if modelMapStr != "" {
		for _, pair := range strings.Split(modelMapStr, ",") {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 {
				modelMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
	}

	return &Service{
		dataDir:  dataDir,
		quiet:    quiet,
		logger:   logger,
		backend:  backend,
		modelMap: modelMap,
	}
}

func defaultModelMap() map[string]string {
	return map[string]string{
		"gemini-2.5-flash":     "llama3.2",
		"gemini-2.5-pro":       "llama3.2",
		"gemini-2.0-flash":     "llama3.2",
		"gemini-1.5-flash":     "llama3.2",
		"gemini-1.5-pro":       "llama3.2",
		"text-embedding-004":   "nomic-embed-text",
		"text-embedding-005":   "nomic-embed-text",
	}
}

func (s *Service) Name() string { return "Vertex AI" }

func (s *Service) Start(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	var handler http.Handler = mux
	if !s.quiet {
		handler = s.loggingMiddleware(mux)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Service) registerRoutes(mux *http.ServeMux) {
	// Vertex AI REST API paths. The genai SDK sends requests like:
	// POST /v1beta1/projects/{p}/locations/{l}/publishers/google/models/{m}:generateContent
	mux.HandleFunc("/", s.handleRequest)
}

func (s *Service) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"service": "localgcp-vertexai"})
			return
		}
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse(405, "METHOD_NOT_ALLOWED", "Only POST is supported"))
		return
	}

	// Parse path: /v1beta1/projects/{p}/locations/{l}/publishers/google/models/{model}:{method}
	path := r.URL.Path
	modelMethod := extractModelAndMethod(path)
	if modelMethod == nil {
		writeJSON(w, http.StatusNotFound, errorResponse(404, "NOT_FOUND", fmt.Sprintf("Unknown path: %s", path)))
		return
	}

	switch modelMethod.method {
	case "generateContent":
		s.handleGenerateContent(w, r, modelMethod.model)
	case "embedContent", "predict":
		s.handleEmbedContent(w, r, modelMethod.model)
	default:
		writeJSON(w, http.StatusNotFound, errorResponse(404, "NOT_FOUND", fmt.Sprintf("Unknown method: %s", modelMethod.method)))
	}
}

type modelAndMethod struct {
	model  string
	method string
}

// extractModelAndMethod parses the Vertex AI REST path to get model name and method.
// Path format: /v.../projects/{p}/locations/{l}/publishers/google/models/{model}:{method}
func extractModelAndMethod(path string) *modelAndMethod {
	// Find "models/" in the path
	idx := strings.Index(path, "/models/")
	if idx < 0 {
		return nil
	}
	rest := path[idx+len("/models/"):]

	// Split on ":" to get model:method
	colonIdx := strings.LastIndex(rest, ":")
	if colonIdx < 0 {
		return nil
	}

	return &modelAndMethod{
		model:  rest[:colonIdx],
		method: rest[colonIdx+1:],
	}
}

func (s *Service) handleGenerateContent(w http.ResponseWriter, r *http.Request, model string) {
	var req vertexGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse(400, "INVALID_ARGUMENT", "Invalid JSON body"))
		return
	}

	// Build intermediate request.
	genReq := &GenerateRequest{Model: model}

	// System instruction.
	if req.SystemInstruction != nil {
		for _, part := range req.SystemInstruction.Parts {
			if part.Text != "" {
				genReq.SystemInstruction = part.Text
			}
		}
	}

	// Messages.
	for _, content := range req.Contents {
		role := content.Role
		if role == "" {
			role = "user"
		}
		for _, part := range content.Parts {
			if part.Text != "" {
				genReq.Messages = append(genReq.Messages, Message{Role: role, Content: part.Text})
			}
		}
	}

	if req.GenerationConfig != nil {
		genReq.Temperature = req.GenerationConfig.Temperature
		genReq.MaxOutputTokens = req.GenerationConfig.MaxOutputTokens
	}

	// Resolve model alias.
	backendModel := s.resolveModel(model)

	resp, err := s.backend.GenerateContent(backendModel, genReq)
	if err != nil {
		s.logger.Printf("generateContent error: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, errorResponse(503, "UNAVAILABLE", err.Error()))
		return
	}

	writeJSON(w, http.StatusOK, vertexGenerateResponse{
		Candidates: []vertexCandidate{{
			Content: vertexContent{
				Role:  "model",
				Parts: []vertexPart{{Text: resp.Text}},
			},
			FinishReason:  resp.FinishReason,
			SafetyRatings: []interface{}{},
		}},
	})
}

func (s *Service) handleEmbedContent(w http.ResponseWriter, r *http.Request, model string) {
	// The SDK uses the predict endpoint with {"instances":[{"content":"text"}]} format.
	// Also support the embedContent format for direct HTTP callers.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse(400, "INVALID_ARGUMENT", "Invalid JSON body"))
		return
	}

	var text string

	if instancesRaw, ok := raw["instances"]; ok {
		// predict format: {"instances":[{"content":"text"}]}
		var instances []map[string]string
		if err := json.Unmarshal(instancesRaw, &instances); err == nil && len(instances) > 0 {
			text = instances[0]["content"]
		}
	} else if contentRaw, ok := raw["content"]; ok {
		// embedContent format: {"content":{"parts":[{"text":"text"}]}}
		var content vertexContent
		if err := json.Unmarshal(contentRaw, &content); err == nil {
			for _, part := range content.Parts {
				if part.Text != "" {
					text += part.Text + " "
				}
			}
			text = strings.TrimSpace(text)
		}
	}

	if text == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse(400, "INVALID_ARGUMENT", "Content text is required"))
		return
	}

	backendModel := s.resolveModel(model)

	resp, err := s.backend.EmbedContent(backendModel, &EmbedRequest{Model: model, Text: text})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse(503, "UNAVAILABLE", err.Error()))
		return
	}

	// Return in predict format (SDK expects this).
	// Convert float64 to float32 for SDK compatibility.
	values32 := make([]float32, len(resp.Values))
	for i, v := range resp.Values {
		values32[i] = float32(v)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"predictions": []map[string]interface{}{
			{
				"embeddings": map[string]interface{}{
					"values": values32,
				},
			},
		},
	})
}

func (s *Service) resolveModel(model string) string {
	if mapped, ok := s.modelMap[model]; ok {
		return mapped
	}
	return model // pass through if no alias
}

// --- Vertex AI JSON types ---

type vertexGenerateRequest struct {
	Contents          []vertexContent    `json:"contents"`
	SystemInstruction *vertexContent     `json:"systemInstruction,omitempty"`
	GenerationConfig  *vertexGenConfig   `json:"generationConfig,omitempty"`
}

type vertexContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []vertexPart `json:"parts"`
}

type vertexPart struct {
	Text string `json:"text,omitempty"`
}

type vertexGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int32   `json:"maxOutputTokens,omitempty"`
}

type vertexGenerateResponse struct {
	Candidates []vertexCandidate `json:"candidates"`
}

type vertexCandidate struct {
	Content       vertexContent   `json:"content"`
	FinishReason  string          `json:"finishReason"`
	SafetyRatings []interface{}   `json:"safetyRatings"`
}

type vertexEmbedRequest struct {
	Content *vertexContent `json:"content,omitempty"`
}

type vertexEmbedResponse struct {
	Embedding vertexEmbedding `json:"embedding"`
}

type vertexEmbedding struct {
	Values []float64 `json:"values"`
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func errorResponse(code int, status, message string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"status":  status,
			"message": message,
		},
	}
}

func (s *Service) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, statusCode: 200}
		next.ServeHTTP(rw, r)
		s.logger.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.statusCode, time.Since(start).Round(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}
