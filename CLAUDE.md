# localgcp

Unified GCP local emulator. Single Go binary, four services, MIT licensed.

Repo: github.com/slokam-ai/localgcp
Domain: localgcp.com

## Build & test

```bash
go build -o localgcp ./cmd/localgcp/   # Build binary
go test ./...                           # Run all tests
go vet ./...                            # Lint
go run ./examples/smoketest/            # SDK integration test (requires localgcp up)
```

## Architecture

- Single Go binary, no runtime dependencies
- Each service is an independent package under `internal/`
- GCS: REST/HTTP on :4443
- Pub/Sub: gRPC on :8085
- Secret Manager: gRPC on :8086
- Firestore: gRPC on :8088
- Storage: in-memory by default, JSON persistence with `--data-dir`
- All services implement `server.Service` interface (Name + Start)

## SDK compatibility rules

Always test with official `cloud.google.com/go/*` client libraries. The client library behavior often differs from documented APIs:
- GCS reads use XML API path (`/{bucket}/{object}`), not JSON API
- Pub/Sub client uses StreamingPull, not unary Pull
- Firestore client uses BatchGetDocuments, not GetDocument
- Secret Manager has no `_EMULATOR_HOST` env var

## Error handling

- REST (GCS): `{"error": {"code": N, "message": "...", "errors": [{"reason": "..."}]}}`
- gRPC: standard status codes with descriptive messages
- Unimplemented RPCs: `codes.Unimplemented` with `"localgcp: {method} not yet supported"`

## Skill routing

When the user's request matches an available skill, ALWAYS invoke it using the Skill
tool as your FIRST action. Do NOT answer directly, do NOT use other tools first.
The skill has specialized workflows that produce better results than ad-hoc answers.

Key routing rules:
- Product ideas, "is this worth building", brainstorming -> invoke office-hours
- Bugs, errors, "why is this broken", 500 errors -> invoke investigate
- Ship, deploy, push, create PR -> invoke ship
- QA, test the site, find bugs -> invoke qa
- Code review, check my diff -> invoke review
- Update docs after shipping -> invoke document-release
- Weekly retro -> invoke retro
- Design system, brand -> invoke design-consultation
- Visual audit, design polish -> invoke design-review
- Architecture review -> invoke plan-eng-review
- Save progress, checkpoint, resume -> invoke checkpoint
- Code quality, health check -> invoke health
