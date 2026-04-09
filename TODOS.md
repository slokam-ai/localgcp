# TODOs

## Phase 2

### ~~Dispatcher: limit response body size~~ (DONE)
- Implemented in `internal/dispatch/dispatcher.go` with `io.LimitReader(resp.Body, 1<<20)`.

### ~~README: prior art section~~ (DONE)
- Added "Prior Art" section to README.md acknowledging fsouza/fake-gcs-server and aertje/cloud-tasks-emulator.

## Vertex AI Emulator

### Streaming support (streamGenerateContent)
- **What:** Translate Ollama NDJSON streaming to Vertex SSE streaming for real-time token output.
- **Why:** Almost all production AI apps use streaming. Without it, the emulator is limited to batch responses.
- **Context:** Ollama streams NDJSON (one JSON object per line). Vertex REST API uses server-sent events. Translation is line-by-line. ~100 lines.
- **Depends on:** MVP Vertex AI emulator (generateContent + embedContent).

### Multi-provider support (OpenAI, Anthropic adapters)
- **What:** Add OpenAI and Anthropic backend adapters alongside Ollama.
- **Why:** Lets developers test model migration without rewriting GCP code.
- **Context:** Each adapter translates Vertex API shape to the provider's API shape. Same Backend interface, different implementation. ~100 lines per adapter.
- **Depends on:** MVP Vertex AI emulator.

## Phase 3

### ~~Firestore Listen: resume tokens~~ (DONE)
- Implemented with global sequence counter, bounded ring buffer (1024 entries), and 8-byte resume tokens.
- Clients reconnecting with valid token get incremental changes; invalid/expired tokens fall back to full snapshot.
