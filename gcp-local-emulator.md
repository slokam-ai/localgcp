# GCP Local Emulator — "Floci for GCP"

## Inspiration

[Floci](https://github.com/hectorvent/floci) is a free, open-source local AWS emulator (lightweight LocalStack alternative) built with Quarkus/GraalVM native. It emulates 20+ AWS services locally for dev/testing with ~24ms startup and ~13MB memory. MIT licensed.

**The gap:** AWS has LocalStack and Floci. GCP has nothing equivalent — only fragmented individual emulators scattered across `gcloud` components with inconsistent APIs and no unified experience.

---

## Core Idea

A unified, single-binary/container local GCP emulator that:

- Starts in milliseconds (GraalVM native compilation)
- Runs all commonly-used GCP services behind one process
- Works out of the box with GCP client libraries (they already support `*_EMULATOR_HOST` env vars)
- Wraps official emulators where they exist, fills gaps with custom implementations
- MIT licensed, no auth tokens, unlimited CI/CD

---

## Tech Stack (Recommended)

- **Quarkus + GraalVM native** — Same as Floci. The startup/memory wins are the killer feature.
- **Fork Floci's core framework** — Storage layer, lifecycle management, plugin architecture, Docker container management, build pipeline all transfer directly.
- **gRPC + REST** — GCP services use both; several core services (Firestore, Spanner, Bigtable) are gRPC-native.
- **Docker socket mounting** — For Cloud Functions/Cloud Run container execution and Cloud SQL database containers.

---

## Service Mapping (AWS Floci -> GCP Equivalent)

| AWS (Floci)       | GCP Equivalent                     | Implementation Approach         | Difficulty |
| ----------------- | ---------------------------------- | ------------------------------- | ---------- |
| S3                | **Cloud Storage (GCS)**            | Custom (JSON + XML API)         | Medium     |
| SQS/SNS           | **Pub/Sub**                        | Wrap official emulator          | Low        |
| DynamoDB           | **Firestore**                      | Wrap official emulator          | Low-Medium |
| Lambda             | **Cloud Functions / Cloud Run**    | Custom (Docker container mgmt)  | Hard       |
| IAM/STS            | **IAM**                            | Custom (roles->permissions)     | Medium     |
| Secrets Manager    | **Secret Manager**                 | Custom                          | Easy       |
| SSM Parameter Store| _(no direct equiv, use Secret Mgr)_| Custom                         | Easy       |
| KMS                | **Cloud KMS**                      | Custom                          | Medium     |
| API Gateway        | **API Gateway / Cloud Endpoints**  | Custom                          | Medium     |
| CloudFormation     | **Deployment Manager**             | Low priority (GCP leans Terraform) | Hard    |
| RDS                | **Cloud SQL**                      | Spin up Postgres/MySQL containers | Medium   |
| EventBridge        | **Eventarc**                       | Custom                          | Medium     |
| CloudWatch         | **Cloud Logging + Monitoring**     | Custom                          | Medium     |
| Kinesis            | **Dataflow / Pub/Sub**             | Custom                          | Medium     |
| Step Functions     | **Workflows**                      | Custom                          | Medium     |
| ElastiCache        | **Memorystore (Redis/Valkey)**     | Spin up Redis containers        | Medium     |
| Cognito            | **Identity Platform / Firebase Auth** | Custom                       | Hard       |

---

## Phased Rollout

### Phase 1 — MVP (covers ~80% of dev workflows)
- Cloud Storage (GCS)
- Pub/Sub (wrap official emulator)
- Secret Manager
- Firestore (wrap official emulator)
- Unified lifecycle, single Docker image, env var config

### Phase 2 — Compute & Data
- Cloud Functions (container-based executor, similar to Floci's Lambda)
- Eventarc (event routing)
- Cloud SQL (spin up Postgres/MySQL/MariaDB containers)
- Cloud Tasks

### Phase 3 — Security & Observability
- IAM (roles, permissions, service accounts)
- Cloud KMS (sign, verify, encrypt/decrypt)
- Cloud Run (container execution)
- Cloud Logging + Cloud Monitoring

### Phase 4 — Advanced
- Workflows (state machine execution)
- Spanner (wrap official emulator)
- Bigtable (wrap official emulator)
- Memorystore

---

## Architectural Decisions

### 1. Unified endpoint vs per-service ports
GCP client libraries use per-service endpoints, not a single regional endpoint like AWS. The emulator would expose multiple ports or use path-based routing. The SDKs already respect `STORAGE_EMULATOR_HOST`, `PUBSUB_EMULATOR_HOST`, `FIRESTORE_EMULATOR_HOST`, etc. — this is a **huge advantage** over the AWS side where SDK compatibility had to be fought for.

### 2. Wrap official emulators where possible
Don't reimplement Pub/Sub or Firestore from scratch. Manage the official emulator processes/containers under the unified lifecycle. Fill in gaps (GCS, IAM, Cloud Functions) with custom code.

### 3. Container management
Reuse Floci's Docker socket approach for:
- Cloud Functions execution (GCP Functions v2 is Cloud Run under the hood)
- Cloud SQL database instances
- Memorystore Redis/Valkey instances

### 4. Storage layer
Port Floci's pluggable storage backends:
- **Memory** — Fastest, no persistence (default for CI)
- **Persistent** — Full disk persistence with JSON serialization
- **Hybrid** — In-memory reads with async disk flush
- **WAL** — Write-ahead logging for durability

### 5. gRPC support
Several GCP services are gRPC-native (Firestore, Spanner, Bigtable). The emulator needs gRPC server support in addition to REST. Quarkus has good gRPC support via `quarkus-grpc`.

---

## Hard Parts

- **gRPC support** — Many GCP services use gRPC natively, not just REST. Need to implement protobuf service definitions.
- **Cloud Functions / Cloud Run** — Container lifecycle, cold starts, event triggers, buildpack simulation.
- **IAM policy evaluation** — GCP's model (roles -> permissions) is simpler than AWS policy documents, but still non-trivial to evaluate correctly.
- **Consistency with real GCP behavior** — Error codes, pagination tokens, eventual consistency semantics, resource naming conventions.
- **Firebase overlap** — Firestore, Auth, etc. have both GCP and Firebase client libraries with slightly different behaviors.

---

## Competitive Advantage

1. **No unified competitor exists** — This would be first-of-its-kind for GCP.
2. **SDK env var support is free** — GCP client libraries already have `*_EMULATOR_HOST` baked in.
3. **Proven architecture** — Floci's Quarkus/GraalVM approach is battle-tested for this exact problem.
4. **Growing demand** — GCP adoption is increasing; developers moving from AWS expect equivalent local tooling.
5. **CI/CD story** — Fast startup + low memory = perfect for ephemeral CI containers.

---

## Name Ideas

- **Cumulo** (cumulus clouds, GCP)
- **Nimbus** (rain cloud, local cloud)
- **Stratos** (stratosphere, GCP platform)
- **Lenticular** (smooth lens-shaped cloud, clean/polished)

---

## References

- Floci: https://github.com/hectorvent/floci
- GCP official emulators: https://cloud.google.com/sdk/gcloud/reference/emulators
- Quarkus gRPC: https://quarkus.io/guides/grpc
- GraalVM native image: https://www.graalvm.org/latest/reference-manual/native-image/
