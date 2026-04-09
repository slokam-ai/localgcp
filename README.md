# localgcp

The unified GCP emulator. One binary, thirteen services, zero cloud bills.

**Now with Vertex AI.** Run your `google.golang.org/genai` code against local LLMs via Ollama. Zero code changes, real inference, no API keys.

```bash
# Install
brew install slokam-ai/tap/localgcp

# Start all native services + Spanner and Bigtable
localgcp up --services=spanner,bigtable

# Your GCP code works unchanged
eval $(localgcp env)
```

```go
// Your existing Vertex AI code — just point BaseURL to localgcp
client, _ := genai.NewClient(ctx, &genai.ClientConfig{
    Project: "my-project", Location: "us-central1",
    Backend: genai.BackendVertexAI,
    HTTPOptions: genai.HTTPOptions{BaseURL: "http://localhost:8090"},
})
resp, _ := client.Models.GenerateContent(ctx, "gemini-2.5-flash",
    genai.Text("Explain quantum computing"), nil)
// Response comes from Gemma/Llama running locally via Ollama
```

Also available via Docker and pre-built binaries:

```bash
# Docker
docker run --rm -p 4443:4443 -p 8085:8085 -p 8086:8086 -p 8088:8088 -p 8089:8089 -p 8090:8090 -p 8091:8091 -p 8092:8092 -p 8093:8093 ghcr.io/slokam-ai/localgcp

# Pre-built binary
# Download from https://github.com/slokam-ai/localgcp/releases

# From source
go install github.com/slokam-ai/localgcp/cmd/localgcp@latest
```

That's it. Your GCP client libraries now talk to localhost instead of Google Cloud.

## Why Vertex AI locally?

Every Vertex AI API call costs money. Every prompt iteration, every integration test, every debug session. localgcp lets you run your GCP AI code against Gemma, Llama, or any Ollama model running on your machine. The official `google.golang.org/genai` SDK works unchanged, just set the `BaseURL` to localgcp. No API keys, no quotas, no bills.

Without Ollama running, localgcp returns deterministic stub responses, perfect for CI/CD pipelines that need to test Vertex AI integration code without burning credits or leaking API keys.

## What it does

localgcp emulates thirteen GCP services locally so you can develop and test without a cloud project, without credentials, and without a bill. Nine services run natively in the binary. Four more (Spanner, Bigtable, Cloud SQL, Memorystore) are orchestrated via Docker containers that start lazily on first use.

| Service | Protocol | Port | Env var |
|---------|----------|------|---------|
| Cloud Storage | REST | 4443 | `STORAGE_EMULATOR_HOST` |
| Pub/Sub | gRPC | 8085 | `PUBSUB_EMULATOR_HOST` |
| Secret Manager | gRPC | 8086 | (manual endpoint config) |
| Firestore | gRPC | 8088 | `FIRESTORE_EMULATOR_HOST` |
| Cloud Tasks | gRPC | 8089 | (manual endpoint config) |
| Vertex AI | REST | 8090 | (manual endpoint config) |
| Cloud KMS | gRPC | 8091 | (manual endpoint config) |
| Cloud Logging | gRPC | 8092 | (manual endpoint config) |
| Cloud Run | gRPC | 8093 | (manual endpoint config) |
| **Orchestrated (Docker required)** | | | |
| Spanner | gRPC | 9010 | `SPANNER_EMULATOR_HOST` |
| Bigtable | gRPC | 9094 | `BIGTABLE_EMULATOR_HOST` |
| Cloud SQL (Postgres) | TCP | 5432 | (standard Postgres) |
| Memorystore (Redis) | TCP | 6379 | (standard Redis) |

## Quick start

### 1. Start the emulator

```bash
localgcp up
```

All native services start in the foreground. Data lives in memory and vanishes when you stop. Press Ctrl+C to stop.

For persistent data across restarts:

```bash
localgcp up --data-dir=./.localgcp
```

### 2. Point your app at it

```bash
eval $(localgcp env)
```

This sets the `*_EMULATOR_HOST` environment variables. Your existing GCP client code works with zero changes for Cloud Storage, Pub/Sub, and Firestore.

**Secret Manager** has no official emulator host env var. Configure the endpoint manually:

```go
client, err := secretmanager.NewClient(ctx,
    option.WithEndpoint("localhost:8086"),
    option.WithoutAuthentication(),
    option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
)
```

```python
from google.cloud import secretmanager
client = secretmanager.SecretManagerServiceClient(
    client_options={"api_endpoint": "localhost:8086"},
)
```

**Vertex AI** uses the new `google.golang.org/genai` SDK with a custom base URL:

```go
client, err := genai.NewClient(ctx, &genai.ClientConfig{
    Project:  "my-project",
    Location: "us-central1",
    Backend:  genai.BackendVertexAI,
    HTTPOptions: genai.HTTPOptions{
        BaseURL: "http://localhost:8090",
    },
})
// Now use client.Models.GenerateContent() as normal.
// Requests go to localgcp -> Ollama (if running) or stub responses.
```

Start Ollama separately for real local inference: `ollama serve` then `ollama pull llama3.2`.

### 3. Use your app normally

Your GCP client libraries work against localgcp with zero code changes (except Secret Manager endpoint). Create buckets, publish messages, store secrets, query documents.

## Features

### Cloud Storage
- Bucket CRUD (create, get, list, delete)
- Object upload (simple, multipart, resumable)
- Object download, metadata, delete, copy
- Object listing with prefix and delimiter (directory-like browsing)
- Signed URL generation and download
- Works with both JSON API and XML API paths

### Pub/Sub
- Topic and subscription management
- Publish and pull (including StreamingPull for official client libraries)
- Push subscriptions (HTTP POST to your endpoint, auto-ack on 2xx)
- Dead letter topic support (forward after N failed deliveries)
- Message acknowledgment and deadline modification
- Fan-out: multiple subscriptions each get all messages

### Secret Manager
- Secret CRUD with versioning
- Version state management (enable, disable, destroy)
- Access by version number or "latest" alias

### Firestore
- Document CRUD with auto-generated IDs
- Real-time listeners (`onSnapshot` / Listen streaming RPC)
- Structured queries: equality, range, `in`, `not-in`, `array-contains`, `array-contains-any`
- OrderBy, limit, offset
- Transactions (begin, commit, rollback)
- Nested collections and subcollections
- BatchGetDocuments for official client library compatibility

### Cloud Tasks
- Queue CRUD (create, get, list, delete, purge)
- Task CRUD (create, get, list, delete)
- HTTP target dispatch to user-configured URLs
- Task scheduling (delay execution by N seconds)
- Retry with configurable max attempts and exponential backoff

### Vertex AI (Gemini API)
- Text generation (generateContent) via local models or stub responses
- Streaming (streamGenerateContent) with real-time token output
- Tool/function calling: declare tools, receive function calls, send results
- Text embeddings (embedContent/predict) with configurable dimensions
- Multi-provider backends: Ollama (default), OpenAI, Anthropic, or stub
- Model alias registry: map Vertex model names to local model names (e.g. `gemini-2.5-flash` -> `llama3.2`)
- Works with the official `google.golang.org/genai` SDK via `HTTPOptions.BaseURL`

### Spanner (orchestrated)
- Runs the official Google Spanner emulator via Docker
- Lazy start: container pulls and starts on first connection (~3s cached, ~30s first time)
- Full Spanner API compatibility via `SPANNER_EMULATOR_HOST=localhost:9010`
- `localgcp up --services=spanner`

### Bigtable (orchestrated)
- Runs the official Google Bigtable emulator via Docker
- Lazy start on first connection
- Full Bigtable API compatibility via `BIGTABLE_EMULATOR_HOST=localhost:9094`
- `localgcp up --services=bigtable`

### Cloud SQL (orchestrated)
- Runs PostgreSQL 16 via Docker
- Standard Postgres connection: `localhost:5432`, user `postgres`, database `localgcp`
- Works with any Postgres client/ORM
- `localgcp up --services=cloudsql`

### Memorystore (orchestrated)
- Runs Redis 7 via Docker
- Standard Redis connection: `localhost:6379`
- Works with any Redis client
- `localgcp up --services=memorystore`

### Cloud KMS
- KeyRing and CryptoKey CRUD
- Symmetric encrypt/decrypt (ENCRYPT_DECRYPT purpose)
- Asymmetric sign and public key retrieval (EC P256, RSA 2048)
- HMAC sign and verify (MAC purpose)
- CryptoKeyVersion management (create, list, destroy)

### Cloud Logging
- Write log entries with severity, labels, and monitored resources
- List log entries with filtering (severity, text payload, log name)
- List distinct log names
- Delete logs by name

### Cloud Run
- Service CRUD (create, get, list, update, delete)
- Immediate operation completion (no polling required)
- Auto-generated service URIs

## CLI reference

```
localgcp up [flags]        Start all services (foreground)
localgcp env [flags]       Print export statements for client libraries
localgcp --version         Print version
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--data-dir` | (in-memory) | Directory for persistent storage |
| `--port-gcs` | 4443 | Cloud Storage port |
| `--port-pubsub` | 8085 | Pub/Sub port |
| `--port-secretmanager` | 8086 | Secret Manager port |
| `--port-firestore` | 8088 | Firestore port |
| `--port-cloudtasks` | 8089 | Cloud Tasks port |
| `--port-vertexai` | 8090 | Vertex AI port |
| `--port-kms` | 8091 | Cloud KMS port |
| `--port-logging` | 8092 | Cloud Logging port |
| `--port-cloudrun` | 8093 | Cloud Run port |
| `--ollama-host` | `http://localhost:11434` | Ollama API host for Vertex AI |
| `--vertex-model-map` | (defaults) | Model aliases (e.g. `gemini-2.5-flash=llama3.2`) |
| `--vertex-backend` | `ollama` | Backend provider: `ollama`, `openai`, `anthropic`, `stub` |
| `--vertex-api-key` | | API key for OpenAI/Anthropic backends |
| `--services` | | Docker-orchestrated services: `spanner,bigtable,cloudsql,memorystore` |
| `--no-docker` | false | Skip all Docker-orchestrated services |
| `--port-spanner` | 9010 | Spanner emulator port |
| `--port-bigtable` | 9094 | Bigtable emulator port |
| `--port-cloudsql` | 5432 | Cloud SQL (Postgres) port |
| `--port-memorystore` | 6379 | Memorystore (Redis) port |
| `--quiet`, `-q` | false | Suppress request logging |

## How it works

localgcp is a single Go binary. Nine services are implemented from scratch in Go with no runtime dependencies. Four additional services (Spanner, Bigtable, Cloud SQL, Memorystore) are orchestrated via Docker containers that start lazily on first use, requiring Docker to be installed. Native service data is stored in memory by default, with optional JSON-file persistence via `--data-dir`. Orchestrated services use ephemeral containers.

GCP client libraries already support `*_EMULATOR_HOST` environment variables. When these are set, the libraries connect to localhost instead of Google Cloud. localgcp uses this mechanism for zero-friction SDK compatibility.

## What's NOT included (yet)

- Cloud Storage: bucket versioning, object compose, IAM policies
- Pub/Sub: exactly-once delivery, message ordering
- Firestore: composite indexes, collection group queries
- Cloud Tasks: App Engine task targets, OIDC/OAuth authentication
- Vertex AI: multimodal (images/audio)
- Secret Manager: IAM bindings, replication policies, rotation
- Cloud KMS: key import, key rotation schedules, raw encrypt/decrypt, asymmetric decrypt
- Cloud Logging: structured payload queries, log sinks, log-based metrics, streaming tail
- Cloud Run: container execution, revisions, traffic splitting, domain mapping
- Orchestrated services: data persistence across restarts, Spanner REST admin API
- IAM/auth enforcement (all requests are accepted)

See [ROADMAP.md](ROADMAP.md) for what's coming next.

## Use cases

- **Local development**: Fast iteration without cloud bills or network latency
- **CI/CD testing**: Ephemeral emulator starts in milliseconds, no cloud credentials needed
- **Offline development**: Works without internet access
- **Integration testing**: Test against real GCP client libraries, not mocks

## Building from source

```bash
git clone https://github.com/slokam-ai/localgcp.git
cd localgcp
go build -o localgcp ./cmd/localgcp/
```

Run tests:

```bash
go test ./...
```

## Prior art

localgcp builds on the shoulders of excellent standalone emulators:

- [fsouza/fake-gcs-server](https://github.com/fsouza/fake-gcs-server) — GCS emulator with broad API coverage
- [aertje/cloud-tasks-emulator](https://github.com/aertje/cloud-tasks-emulator) — Cloud Tasks emulator with App Engine support

localgcp's differentiator is the **unified single-binary** approach: one process, thirteen services, zero configuration. If you only need one service and want deeper API coverage, these standalone projects are great choices.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT. See [LICENSE](LICENSE).

---

Built by [yogirk](https://github.com/yogirk). Visit [localgcp.com](https://localgcp.com) for more.
