# Roadmap

## Phase 1 — MVP (done)

Core services that cover ~80% of typical GCP dev workflows.

- [x] Cloud Storage (GCS) — bucket/object CRUD, multipart + resumable uploads
- [x] Pub/Sub — topics, subscriptions, publish, pull, StreamingPull, ack
- [x] Secret Manager — secrets with versioning, state management
- [x] Firestore — document CRUD, queries (equality + range), transactions
- [x] Unified CLI (`localgcp up`, `localgcp env`)
- [x] In-memory storage with optional JSON persistence (`--data-dir`)
- [x] Port conflict detection with clear error messages
- [x] Request logging (default on, `--quiet` for CI)

## Phase 2 — Depth + strategic expansion

Fill the gaps that matter most for real-world adoption.

- [x] Distribution: goreleaser, GitHub Actions CI, Homebrew, Docker (v0.1.0)
- [x] Firestore Store lock refactor + deep copy + race fix + 15 Store unit tests
- [x] Firestore real-time listeners (`onSnapshot` / `Listen` streaming RPC)
- [x] Firestore `in`, `not-in`, `array-contains`, `array-contains-any` operators
- [x] Pub/Sub push subscriptions (server POSTs to user endpoint)
- [x] Pub/Sub dead letter topic support
- [x] Cloud Tasks (queue/task CRUD, HTTP dispatch, scheduling)
- [x] GCS signed URLs
- [x] README prior art section
- [ ] ~~Eventarc~~ (deferred to Phase 3, depends on Cloud Run)
- [ ] ~~Firestore collection group queries~~ (deferred to Phase 3, complex indexing)

## AI/ML Services

- [x] Vertex AI Gemini API — generateContent + embeddings via Ollama proxy
- [x] Stub backend for CI/CD (deterministic responses, no model needed)
- [x] Model alias registry (gemini-2.5-flash -> llama3.2, etc.)
- [x] Streaming (streamGenerateContent) — Ollama NDJSON to Vertex SSE
- [x] Multi-provider backends (OpenAI, Anthropic adapters)
- [x] Tool/function calling support
- [ ] Multimodal (image/audio input)

## Phase 3 — Security and observability

- [x] Cloud KMS (encrypt/decrypt, sign/verify, MAC, key management)
- [x] Cloud Logging (log ingestion, query, filtering, delete)
- [x] Cloud Run (service CRUD with immediate operations)

## Phase 3b — IAM (deferred, needs design)

- [ ] IAM (roles, permissions, service accounts) — opt-in enforcement
  - Standalone IAM API first (service account CRUD, get/set policies, testIamPermissions)
  - Enforcement middleware behind `--iam` flag second (cross-cutting, touches all services)

## Phase 4 — Complex services (two-tier strategy)

Services that ARE databases get a two-tier approach:
- **Tier 1 (default):** Lightweight custom implementation in the single binary
- **Tier 2 (opt-in):** Docker container wrapping the official Google emulator for high fidelity

| Service | Tier 1 (custom) | Tier 2 (wrapped) |
|---------|----------------|-----------------|
| Spanner | Basic SQL subset | Official emulator via Docker |
| Bigtable | Key-value operations | Official emulator via Docker |
| Cloud SQL | — | Postgres/MySQL container |
| Memorystore | — | Redis container |

Tier 2 is activated with `localgcp up --high-fidelity` or `localgcp up --wrap=spanner,bigtable`.

## Distribution milestones

- [x] GitHub Releases with pre-built binaries (goreleaser)
- [x] Homebrew cask (`brew install slokam-ai/tap/localgcp`)
- [x] Docker multi-arch image (`ghcr.io/slokam-ai/localgcp`)
- [x] GitHub Actions CI (test on push) + release (on tag)
- [x] Landing page at localgcp.com

## Ideas (not committed)

- Testcontainers integration for Java/Go/Python test frameworks
- Web UI for inspecting emulator state
- Terraform provider compatibility
- Multi-language SDK integration tests (Python, Java, Node.js)
- Cloud Functions v2 (container-based executor)
- Daemon mode (`localgcp up -d`)
