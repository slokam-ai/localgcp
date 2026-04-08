# TODOs

## Phase 2

### Dispatcher: limit response body size
- **What:** Add `io.LimitedReader` (cap 1MB) to HTTP response body reads in the shared dispatcher.
- **Why:** Prevents OOM if a push/task endpoint returns a huge error response.
- **Context:** The dispatcher is used by both Pub/Sub push and Cloud Tasks. Without a limit, a misbehaving endpoint could cause memory spikes. 5 lines of code.
- **Depends on:** Shared dispatcher implementation (internal/dispatch/).

### README: prior art section
- **What:** Add a "Prior Art" section to README.md acknowledging existing community emulators with links.
- **Why:** Intellectual honesty and differentiation. Users who know about standalone emulators should see how localgcp compares.
- **Context:** Known prior art: [fsouza/fake-gcs-server](https://github.com/fsouza/fake-gcs-server) (GCS), [aertje/cloud-tasks-emulator](https://github.com/aertje/cloud-tasks-emulator) (Cloud Tasks, 327 stars). localgcp differentiates via the unified single-binary approach.
- **Depends on:** Nothing. Bundle with the distribution/README work in Week 1-2.

## Phase 3

### Firestore Listen: resume tokens
- **What:** Add resume token support to the Firestore Listen implementation.
- **Why:** Without resume tokens, every client reconnection triggers a full snapshot resend. Fine for local dev with small collections, but a known behavioral gap vs real Firestore.
- **Context:** The Go SDK sends resume tokens on reconnect. Adding them requires sequence number tracking in the watcher registry (~100 lines). Not needed for Phase 2 MVP.
- **Depends on:** Phase 2 Listen MVP implementation.
