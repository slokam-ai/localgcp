# Contributing to localgcp

Thanks for your interest in contributing. Here's how to get started.

## Development setup

```bash
git clone https://github.com/slokam-ai/localgcp.git
cd localgcp
go build ./...
go test ./...
```

Requirements: Go 1.22+ (developed on 1.26)

## Project structure

```
cmd/localgcp/              CLI entry point (cobra)
internal/
  server/                  Multi-service lifecycle, shutdown, port management
  auth/                    Dummy credential generation
  gcs/                     Cloud Storage emulator (REST/HTTP)
  pubsub/                  Pub/Sub emulator (gRPC)
  secretmanager/           Secret Manager emulator (gRPC)
  firestore/               Firestore emulator (gRPC, including query engine)
  cloudtasks/              Cloud Tasks emulator (gRPC)
  vertexai/                Vertex AI emulator (REST, Ollama/OpenAI/Anthropic backends)
  kms/                     Cloud KMS emulator (gRPC)
  logging/                 Cloud Logging emulator (gRPC)
  cloudrun/                Cloud Run emulator (gRPC)
  orchestrator/            Docker orchestrator for Spanner, Bigtable, Cloud SQL, Memorystore
  dispatch/                Shared HTTP dispatcher with retry (used by Cloud Tasks)
examples/smoketest/        SDK integration test using official GCP client libraries
examples/vertexai/         Vertex AI SDK example
website/                   Landing page + 14 documentation pages (static HTML)
```

Each native service follows the same pattern:
- `store.go` — data model, in-memory storage, JSON persistence
- `service.go` — server implementation (HTTP handlers or gRPC server)
- `service_test.go` — tests

The orchestrator package follows a different pattern:
- `runtime.go` — `ContainerRuntime` interface + Docker SDK implementation
- `lazy.go` — `LazyService` (TCP proxy that starts Docker containers on first connection)
- `config.go` — per-service Docker image configs + `WithDataDir` for persistence

## Adding a new native service

1. Create a directory under `internal/` (e.g., `internal/newservice/`)
2. Implement the `server.Service` interface:
   ```go
   type Service interface {
       Name() string
       Start(ctx context.Context, addr string) error
   }
   ```
3. Register it in `cmd/localgcp/main.go` in the `upCmd()` function
4. Add the port flag and `localgcp env` output
5. Write tests that exercise the service via its protocol (HTTP or gRPC)
6. Run the smoke test to verify SDK compatibility: `go run ./examples/smoketest/`

## Adding an orchestrated service

If the service has an official Docker-based emulator, you can add it as an orchestrated service instead of implementing it from scratch:

1. Add a `ContainerConfig` entry in `internal/orchestrator/config.go`
2. Add it to the `ServiceRegistry` map
3. Add the port config in `internal/server/server.go` and CLI flags in `cmd/localgcp/main.go`
4. Add a documentation page in `website/docs/`

Orchestrated services start lazily (container only pulls/starts on first connection).

## Testing

Every service must have:

- **Unit tests** for each endpoint/RPC (happy path + error cases)
- **SDK compatibility**: Test with the official `cloud.google.com/go/*` client library, not just raw HTTP/gRPC. The client library's behavior often differs from the documented API.

Known gotchas (learn from our mistakes):
- GCS: Go client uses XML API path (`GET /{bucket}/{object}`) for reads, not the JSON API
- Pub/Sub: Go client uses `StreamingPull` (streaming), not unary `Pull`
- Firestore: Go client uses `BatchGetDocuments` (streaming), not unary `GetDocument`
- Vertex AI: genai SDK uses `predict` endpoint for embeddings, not `embedContent`
- Vertex AI: genai SDK requires GCP credentials even when BaseURL points to localhost (skip SDK tests in CI)

Orchestrator tests use a mock `ContainerRuntime` (no Docker needed in CI). Integration tests with real Docker are tagged `//go:build integration`.

Run all tests:

```bash
go test ./... -v
```

## Code style

- Standard Go formatting (`gofmt`)
- `go vet` must pass with no warnings
- Error messages should match GCP's format so client libraries parse them correctly
- Unimplemented RPCs return `codes.Unimplemented` with message `"localgcp: {method} not yet supported"`
- Native services have no external runtime dependencies (the binary runs standalone)
- Orchestrated services require Docker but are opt-in via `--services` flag

## Pull requests

- One feature or fix per PR
- Include tests for new functionality
- Run `go test ./...` and `go vet ./...` before submitting
- Keep PRs focused — if you find an unrelated issue, open a separate PR

## Reporting issues

When reporting a bug, include:
- localgcp version (`localgcp --version`)
- Go version (`go version`)
- OS and architecture
- The GCP client library and version you're using
- Steps to reproduce
- Expected vs actual behavior

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
