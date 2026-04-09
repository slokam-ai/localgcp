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

### ~~`localgcp pull` command~~ (DONE)
- `localgcp pull [--services=spanner,bigtable]` pre-fetches Docker images.
- Pulls all 4 images by default, or specific services via `--services` flag.

### ~~Data persistence for orchestrated containers~~ (DONE)
- `--data-dir` mounts host volumes into Docker containers for Cloud SQL and Memorystore.
- Redis gets `appendonly yes` mode when persisting. Postgres mounts `/var/lib/postgresql/data`.
- Spanner and Bigtable emulators don't support persistence (ephemeral only).
