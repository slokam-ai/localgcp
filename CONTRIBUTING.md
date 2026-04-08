# Contributing to localgcp

Thanks for your interest in contributing. Here's how to get started.

## Development setup

```bash
git clone https://github.com/slokam-ai/localgcp.git
cd localgcp
go build ./...
go test ./...
```

Requirements: Go 1.22+

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
examples/smoketest/        SDK integration test using official GCP client libraries
```

Each service follows the same pattern:
- `store.go` — data model, in-memory storage, JSON persistence
- `service.go` — server implementation (HTTP handlers or gRPC server)
- `service_test.go` or `gcs_test.go` — tests

## Adding a new service

1. Create a directory under `internal/` (e.g., `internal/cloudtasks/`)
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

## Testing

Every service must have:

- **Unit tests** for each endpoint/RPC (happy path + error cases)
- **SDK compatibility**: Test with the official `cloud.google.com/go/*` client library, not just raw HTTP/gRPC. The client library's behavior often differs from the documented API.

Known gotchas (learn from our mistakes):
- GCS: Go client uses XML API path (`GET /{bucket}/{object}`) for reads, not the JSON API
- Pub/Sub: Go client uses `StreamingPull` (streaming), not unary `Pull`
- Firestore: Go client uses `BatchGetDocuments` (streaming), not unary `GetDocument`

Run all tests:

```bash
go test ./... -v
```

## Code style

- Standard Go formatting (`gofmt`)
- `go vet` must pass with no warnings
- Error messages should match GCP's format so client libraries parse them correctly
- Unimplemented RPCs return `codes.Unimplemented` with message `"localgcp: {method} not yet supported"`
- No external runtime dependencies (the binary must run standalone)

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
