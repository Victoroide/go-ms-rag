# Deployment

## Container Image

The service is packaged using a multi-stage Docker build that outputs a minimal, non-root runtime container based on Alpine Linux.

### Build Stages

1. **`base`**: Builds on `golang:1.27.3-alpine3.23`, installs compilation tools (`gcc`, `musl-dev`, `git`), and downloads Go modules.
2. **`test`**: Runs static checks (`go vet ./...`) and the short suite with data race instrumentation (`go test -short ./... -count=1 -race`); Docker-backed tests run in full on the host or CI runner.
3. **`lint`**: Compiles and executes `golangci-lint` (v2.15.0) against `.golangci.yml`.
4. **`builder`**: Compiles the statically linked binary (`CGO_ENABLED=0 go build -o /bin/server ./cmd/server`).
5. **`runtime`**: Builds on `alpine:3.23.4`. Installs CA certificates and timezone data, creates a dedicated system user and group (`app`, numeric UID/GID `10001:10001`), copies the compiled server binary containing the embedded migrations, and sets the non-root user as the execution user.

For host verification, `make vet`, `make lint` and `make test` run directly with the local Go toolchain; the full test suite needs a Docker daemon. `make ci` runs those gates in order and then builds the runtime image. The image build's short suite skips Docker-backed database tests, with an explicit daemon requirement message.

To compile the runtime image:

```sh
docker build --target runtime -t quarry-rag:local .
```

To run the container with an environment file:

```sh
docker run -d \
  --name quarry-rag \
  --user 10001:10001 \
  --env-file .env \
  -p 8081:8081 \
  quarry-rag:local
```

---

## Document Extraction Processes

Each concurrent multipart upload starts at most one extraction child process. The service binary must be able to execute itself; the runtime filesystem and process policy must allow this. `EXTRACT_CONCURRENCY` defaults to two, and a slot is held while the upload is read as well as while its document is parsed.

Size container memory to at least `base + EXTRACT_CONCURRENCY × EXTRACT_MEMORY_LIMIT_MB`, where `base` covers the server, buffered uploads and background ingestion. The child memory setting is a soft Go limit; allow additional headroom for runtime overhead and temporary allocations. Each child has a 64 MiB maximum stack and a 30-second default wall-clock timeout. On Linux it attempts to set `oom_score_adj=1000` so that memory pressure preferentially terminates the parser. Failure to execute a child makes document uploads return HTTP 503.

## Required External Services

In production deployments (`GO_RAG_FAKE_MODE=false`), the microservice connects to four external cloud services, configured via environment variables.

### 1. PostgreSQL (Metadata and Chunk Store)

- **Version**: PostgreSQL 14 or later.
- **Extensions**: `pgcrypto` (applied automatically by migration 001).
- **Configuration**: Provided via `DATABASE_URL`.
- **Purpose**: Stores document records, status indicators, chunk texts, token counts, and SHA-256 content fingerprints.
- **Operations**: Versioned startup migrations, duplicate fingerprint lookups, chunk insertion transactions, document status tracking, and inventory listings. Replicas may start concurrently: a transaction-scoped advisory lock serializes migration checks and changes. Applied versions and checksums are recorded in `schema_migrations`; restarts with no pending changes skip the migration SQL.

### 2. Pinecone (Vector Index)

- **Configuration**: `PINECONE_API_KEY`, `PINECONE_INDEX_NAME`, and optional `PINECONE_ENVIRONMENT`.
- **Startup Requirements**: The index dimension must equal `EMBEDDING_DIMENSIONS` (default: 1024), the metric must be `cosine`, and the index must be ready. Startup fails if any requirement is unmet.
- **Request Limits**: Upserts use at most `PINECONE_UPSERT_BATCH_SIZE` records (default 100) and 1.5 MiB of estimated encoded data per batch. `PINECONE_TIMEOUT` bounds each attempt (default 10 seconds), and `PINECONE_MAX_ATTEMPTS` limits write and deletion attempts (default 5).
- **Deletion**: If metadata-filter deletion returns `InvalidArgument`, `Unimplemented`, or `FailedPrecondition`, the service queries the default namespace with an exact `doc_id` filter and deletes the returned identifiers in batches of at most 1,000. Each identifier is deleted once per operation, so stale query results do not repeat deletions. Enumeration stops when no new identifiers appear or after deleting a result with fewer than 1,000 matches, and otherwise fails after 10 rounds. Missing vectors and namespaces are accepted as already deleted.
- **Purpose**: High-dimensional vector search. Chunks are upserted only into the default namespace (`""`), with `doc_id` metadata identifying each document. Per-document namespaces created by earlier versions are removed when their document is deleted.

### 3. AWS Bedrock (Embedding and LLM Models)

- **Configuration**: `AWS_REGION` and model identifiers (`BEDROCK_EMBED_MODEL`, `BEDROCK_PRIMARY_MODEL`, `BEDROCK_FALLBACK_MODEL`). Static `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` are optional and must be configured together; temporary static credentials also accept `AWS_SESSION_TOKEN`.
- **Credentials**: IAM roles are supported. Without static keys, AWS clients use the default credential chain, including environment credentials, shared configuration files, web identity, container roles and instance roles.
- **Retries and Deadlines**: Bedrock and S3 use the same retry policy, with `AWS_MAX_ATTEMPTS` attempts (default 4), up to 10 seconds of backoff, no client-side retry quota and retries for `ModelTimeoutException` and `ModelNotReadyException`. Each embedding call has an `EMBED_TIMEOUT` deadline (default 15 seconds), including waiting for a concurrency slot and retries.
- **Required Model Access**:
  - Titan Embed Text V2 (`amazon.titan-embed-text-v2:0`): generates 1024-dimensional normalized text embeddings.
  - Nova Micro (`amazon.nova-micro-v1:0`): fast, low-latency reasoning and synthesis.
  - Llama 4 Maverick (`us.meta.llama4-maverick-17b-instruct-v1:0`): synthesis and comparison across multiple retrieved chunks.

### 4. AWS S3 (Raw Object Storage)

- **Configuration**: Set `S3_BUCKET` explicitly to a bucket you control, using the same AWS credentials configured for Bedrock. Set optional `S3_EXPECTED_BUCKET_OWNER` to the owner's 12-digit AWS account ID. An empty bucket disables original file storage and logs a startup warning.
- **Startup Verification**: Client initialization and `HeadBucket` share a 10-second deadline. Access denied (403), a missing bucket (404), or an expected-owner mismatch prevents startup. Transient errors log a warning and allow startup to continue; an initialized client remains available for later requests.
- **Purpose**: Stores original binary uploads (PDF, DOCX, DOC) and generates presigned GET download links valid for up to 15 minutes. A download signed with temporary credentials stops working when those credentials expire, which can be before 15 minutes. Downloads use attachment disposition with the stored filename and an extension-derived content type. Downloaders need no expected-owner header.
- **IAM Permissions Required**:
  - `s3:PutObject` on `arn:aws:s3:::{bucket}/documents/*`
  - `s3:GetObject` on `arn:aws:s3:::{bucket}/documents/*`
  - `s3:DeleteObject` on `arn:aws:s3:::{bucket}/documents/*`
  - `s3:ListBucket` on `arn:aws:s3:::{bucket}`, required by the startup and health `HeadBucket` checks.

---

## Health Probes and Readiness

The service exposes three unauthenticated probe endpoints:

- Use `GET /livez` for startup and liveness checks. It always returns HTTP 200 with `status: "ok"` while serving requests and makes no dependency calls.
- Use `GET /readyz` for readiness checks. It returns HTTP 200 with `status: "ready"` when the cached PostgreSQL check succeeds, HTTP 503 with `status: "unavailable"` when that check fails, and HTTP 503 with `status: "draining"` during shutdown.
- Use `GET /health` for diagnostics. It returns HTTP 200 with `status: "ok"` when all configured dependencies respond, or HTTP 503 with `status: "degraded"` otherwise. `postgres` reports database health; `neon` is a deprecated alias.
- Diagnostics and readiness share a snapshot cached for `HEALTH_CACHE_TTL` (default 10 seconds). Concurrent callers share one refresh after expiry. Checks run concurrently against Pinecone, PostgreSQL and configured S3 storage, each with a 5-second timeout.

---

## Graceful Shutdown

The service handles graceful shutdown when receiving `SIGINT` or `SIGTERM`:

1. Marks readiness as draining while continuing to serve requests for `SHUTDOWN_DRAIN_DELAY` (default 5 seconds). A second termination signal skips the remaining delay.
2. Stops accepting new inbound HTTP requests and drains active requests with a 15-second deadline.
3. Stops ingestion claims and waits up to 15 seconds for active jobs, then cancels them and releases their leases within a detached 5-second deadline. Pending jobs remain in PostgreSQL.
4. Closes active Pinecone index client connections.
5. Closes the PostgreSQL connection pool.

Set the termination grace period to at least `SHUTDOWN_DRAIN_DELAY + 40s` (45 seconds with the default delay) to allow traffic removal, HTTP requests, worker completion and lease release to settle before sending `SIGKILL`. No preStop hook is needed.

Replicas share the PostgreSQL ingestion queue and claim jobs with renewable leases. Accepted content survives restarts. Graceful shutdown releases unfinished leases without consuming an attempt; after abrupt termination, another replica reclaims expired leases. No pending document is dropped.

---

## Service Token Rotation

The `SERVICE_TOKEN` environment variable defines the secret expected in the `X-Service-Token` header. To rotate the token in a production deployment without downtime:

1. **Deploy calling services with dual-token support or phased updating**: Update the calling application to present the new token value, or stage the rotation in coordinated steps.
2. **Update the microservice environment**: Update the `SERVICE_TOKEN` environment variable on the microservice instance.
3. **Restart the microservice**: Perform a rolling restart of the microservice. In-flight requests presenting the existing token complete within the graceful shutdown window, while subsequent requests use the new token.
4. **Verify operations**: Send a probe request with the new token to `POST /query` or `GET /documents` to confirm authentication succeeds.
