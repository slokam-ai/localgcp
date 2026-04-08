# TODOs

## Phase 2

### ~~Dispatcher: limit response body size~~ (DONE)
- Implemented in `internal/dispatch/dispatcher.go` with `io.LimitReader(resp.Body, 1<<20)`.

### README: prior art section
- **What:** Add a "Prior Art" section to README.md acknowledging existing community emulators with links.
- **Why:** Intellectual honesty and differentiation. Users who know about standalone emulators should see how localgcp compares.
- **Context:** Known prior art: [fsouza/fake-gcs-server](https://github.com/fsouza/fake-gcs-server) (GCS), [aertje/cloud-tasks-emulator](https://github.com/aertje/cloud-tasks-emulator) (Cloud Tasks, 327 stars). localgcp differentiates via the unified single-binary approach.
- **Depends on:** Nothing. Bundle with the distribution/README work in Week 1-2.

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

### Firestore Listen: resume tokens
- **What:** Add resume token support to the Firestore Listen implementation.
- **Why:** Without resume tokens, every client reconnection triggers a full snapshot resend. Fine for local dev with small collections, but a known behavioral gap vs real Firestore.
- **Context:** The Go SDK sends resume tokens on reconnect. Adding them requires sequence number tracking in the watcher registry (~100 lines). Not needed for Phase 2 MVP.
- **Depends on:** Phase 2 Listen MVP implementation.
