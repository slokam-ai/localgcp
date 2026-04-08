Diving straight into the MVP is the right move. Momentum is everything right now, and getting a working `localgcp` binary into developers' hands will give you the feedback loop you need. 

As your architect, my goal here is to point out the landmines before you step on them. Building an emulator isn't just about mocking APIs; it's about perfectly replicating the quirks, errors, and networking expectations of the official GCP SDKs. 

Here is your architectural "gotcha" and suggestion document to use as context for refining your spec.

***

# LocalGCP — Phase 1 MVP: Architectural Gotchas & Specs

## 1. The Startup Time Trap (GraalVM vs. Process Wrapping)
You chose Quarkus + GraalVM for millisecond startup times, which is perfect for CI/CD. However, if your MVP strategy relies on wrapping the official Google emulators (like Firestore and Pub/Sub), you will lose this advantage immediately.

* **The Gotcha:** The Quarkus orchestrator will boot in 24ms, but the underlying Firestore emulator (a heavy Java process) might take 3-5 seconds to bind to its port and become responsive. If your CLI reports "Ready" before the child processes are actually accepting traffic, CI/CD pipelines will fail with `Connection Refused` errors.
* **The Solution:** * **Health Checks:** Your orchestrator *must* aggressively poll the child processes and block the "Ready" signal until all wrapped emulators return a `200 OK` on their respective health/discovery endpoints.
    * **Lazy Loading (Idea):** Allow users to start `localgcp` instantly, but only spin up the heavy wrapped emulators on the first incoming request to that specific service. 

## 2. Networking: Single Port vs. Multi-Port
AWS LocalStack multiplexes everything over a single port (`4566`), which requires the AWS SDK to do some heavy lifting with endpoint overrides. GCP is different.

* **The Gotcha:** GCP SDKs heavily rely on distinct environment variables (`PUBSUB_EMULATOR_HOST`, `FIRESTORE_EMULATOR_HOST`). Because of this, trying to force all GCP services through a single port (like LocalStack) is actually an anti-pattern for GCP. It will make gRPC routing a nightmare.
* **The Solution:** Embrace the multi-port architecture. Let GCS run on `4443`, Pub/Sub on `8085`, and Firestore on `8080`. 
* **DX Suggestion:** Build a `localgcp env` command that outputs the exact export statements developers need to run in their terminal to point all SDKs to your ports instantly.
    ```bash
    eval $(localgcp env)
    ```

## 3. The Authentication Phantom
Even when pointed at an emulator, GCP client libraries are notoriously stubborn about finding valid credentials.

* **The Gotcha:** If a developer's machine doesn't have `GOOGLE_APPLICATION_CREDENTIALS` set, or lacks an active `gcloud auth application-default login` state, the SDKs will often hang trying to reach the GCP Metadata server (`169.254.169.254`) before they even attempt to hit your emulator.
* **The Solution:** `localgcp` needs to ship with a dummy, hardcoded JSON credential file. When a user runs the tool, explicitly instruct them (or auto-inject) a path to this dummy file so the SDKs bypass the auth-check phase and immediately route to your localhost endpoints.

## 4. Phase 1 Service-Specific Landmines

### Cloud Storage (GCS)
* **The Gotcha:** GCS has two APIs: a JSON API and an XML API. Different SDKs and tools (like `gsutil` vs client libraries) use them interchangeably. Furthermore, GCS heavily relies on multi-part uploads for large files.
* **The Solution:** For MVP, only implement the JSON API, as that covers 90% of modern client library usage. Fail gracefully with a clear "Not Implemented in MVP" error if an XML request hits the router. Use a simple local filesystem directory (`/tmp/localgcp/storage`) to back the buckets.

### Pub/Sub (Wrapped)
* **The Gotcha:** The official Pub/Sub emulator does *not* persist data across restarts. It holds everything in memory.
* **The Solution:** Document this clearly. If developers want persistence, you will eventually need to replace the official wrapper with your own custom Quarkus implementation. For MVP, memory-only is fine and actually preferred for CI/CD.

### Secret Manager (Custom)
* **The Gotcha:** Secret Manager relies heavily on IAM permissions for access control in production.
* **The Solution:** Ignore IAM completely for the MVP. Make every secret globally readable/writable within the emulator. Focus strictly on the CRUD operations for Secrets and SecretVersions.

### Firestore (Wrapped)
* **The Gotcha:** Firestore is entirely gRPC-based and uses complex bi-directional streaming for realtime updates (snapshots).
* **The Solution:** Wrapping the official emulator is 100% the right call here. Building a custom Firestore emulator that perfectly mimics its indexing and realtime stream behavior is a multi-year project on its own. Do not try to rewrite this.

## 5. Storage State (The Memory vs. Disk Divide)
Developers will use this for two distinct workflows: ephemeral CI runs and long-lived local development.

* **The Gotcha:** If you write state to disk by default, CI runs will leave trash behind. If you keep everything in memory, local developers will be frustrated when they restart the CLI and lose their test database.
* **The Solution:** Make state explicit. 
    * `localgcp start` -> purely in-memory (data vanishes on exit).
    * `localgcp start --data-dir=./.localgcp` -> writes storage/secrets to disk.

***

How does this align with what you were envisioning for the architecture? If you want, we can drill down into the exact Quarkus dependencies (like `quarkus-grpc` vs `quarkus-resteasy-reactive`) you'll need to handle the dual API nature of these services.
