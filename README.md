# localgcp

The first unified GCP emulator. One binary, five services, zero cloud bills.

```bash
# Homebrew (macOS/Linux)
brew install slokam-ai/tap/localgcp

# Docker
docker run --rm -p 4443:4443 -p 8085:8085 -p 8086:8086 -p 8088:8088 -p 8089:8089 ghcr.io/slokam-ai/localgcp

# Pre-built binary
# Download from https://github.com/slokam-ai/localgcp/releases

# From source
go install github.com/slokam-ai/localgcp/cmd/localgcp@latest
```

```bash
localgcp up
```

That's it. Your GCP client libraries now talk to localhost instead of Google Cloud.

## What it does

localgcp emulates core GCP services locally so you can develop and test without a cloud project, without credentials, and without a bill.

| Service | Protocol | Port | Env var |
|---------|----------|------|---------|
| Cloud Storage | REST | 4443 | `STORAGE_EMULATOR_HOST` |
| Pub/Sub | gRPC | 8085 | `PUBSUB_EMULATOR_HOST` |
| Secret Manager | gRPC | 8086 | (manual endpoint config) |
| Firestore | gRPC | 8088 | `FIRESTORE_EMULATOR_HOST` |
| Cloud Tasks | gRPC | 8089 | (manual endpoint config) |

## Quick start

### 1. Start the emulator

```bash
localgcp up
```

All four services start in the foreground. Data lives in memory and vanishes when you stop. Press Ctrl+C to stop.

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
| `--quiet`, `-q` | false | Suppress request logging |

## How it works

localgcp is a single Go binary with no runtime dependencies. Each GCP service is implemented from scratch in Go (no wrapping of Google's official Java-based emulators). Data is stored in memory by default, with optional JSON-file persistence via `--data-dir`.

GCP client libraries already support `*_EMULATOR_HOST` environment variables. When these are set, the libraries connect to localhost instead of Google Cloud. localgcp uses this mechanism for zero-friction SDK compatibility.

## What's NOT included (yet)

- Cloud Storage: bucket versioning, object compose, IAM policies
- Pub/Sub: exactly-once delivery, message ordering
- Firestore: composite indexes, collection group queries, resume tokens for listeners
- Cloud Tasks: App Engine task targets, OIDC/OAuth authentication
- Secret Manager: IAM bindings, replication policies, rotation
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

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT. See [LICENSE](LICENSE).

---

Built by [Slokam AI](https://slokam.ai). Visit [localgcp.com](https://localgcp.com) for more.
