# TODOs

## Phase 2

### ~~Dispatcher: limit response body size~~ (DONE)
- Implemented in `internal/dispatch/dispatcher.go` with `io.LimitReader(resp.Body, 1<<20)`.

### ~~README: prior art section~~ (DONE)
- Added "Prior Art" section to README.md acknowledging fsouza/fake-gcs-server and aertje/cloud-tasks-emulator.

## Vertex AI Emulator

### ~~Streaming support (streamGenerateContent)~~ (DONE)
- Ollama NDJSON to Vertex JSON array streaming. Stub backend splits into word-level chunks.

### ~~Multi-provider support (OpenAI, Anthropic adapters)~~ (DONE)
- OpenAI and Anthropic backend adapters via `--vertex-backend` flag.
- Full streaming and tool/function calling support across all backends.

## Phase 3

### ~~Firestore Listen: resume tokens~~ (DONE)
- Implemented with global sequence counter, bounded ring buffer (1024 entries), and 8-byte resume tokens.
- Clients reconnecting with valid token get incremental changes; invalid/expired tokens fall back to full snapshot.

## Phase 4 — Docker Orchestrator

### `localgcp pull` command
- **What:** `localgcp pull [--services=spanner,bigtable]` pre-fetches Docker images so first-request latency is 3s (container start) not 30-60s (pull + start).
- **Why:** Major UX improvement. Eliminates cold-pull surprise, enables offline setup.
- **Context:** ~30 lines using ContainerRuntime.Pull(). Design doc lists as next step after Spanner MVP.
- **Depends on:** ContainerRuntime interface (v0.5.0 orchestrator package).

### Data persistence for orchestrated containers
- **What:** Map `--data-dir` to Docker volume mounts so Spanner/Bigtable data survives container restart.
- **Why:** Native services already support `--data-dir`. Orchestrated services should match for consistency.
- **Context:** Each emulator has different persistence flags. Spanner has limited persistence support. v0.5.0 uses ephemeral containers.
- **Depends on:** Orchestrator MVP (v0.5.0).
