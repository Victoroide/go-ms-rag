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
- **Index Dimensions**: Must match `EMBEDDING_DIMENSIONS` (default: 1024).
- **Metric**: Cosine similarity.
- **Purpose**: High-dimensional vector search. Chunks are upserted only into the default namespace (`""`), with `doc_id` metadata identifying each document. Per-document namespaces created by earlier versions are removed when their document is deleted.

### 3. AWS Bedrock (Embedding and LLM Models)

- **Configuration**: `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and model identifiers (`BEDROCK_EMBED_MODEL`, `BEDROCK_PRIMARY_MODEL`, `BEDROCK_FALLBACK_MODEL`).
- **Required Model Access**:
  - Titan Embed Text V2 (`amazon.titan-embed-text-v2:0`): generates 1024-dimensional normalized text embeddings.
  - Nova Micro (`amazon.nova-micro-v1:0`): fast, low-latency reasoning and synthesis.
  - Llama 4 Maverick (`us.meta.llama4-maverick-17b-instruct-v1:0`): synthesis and comparison across multiple retrieved chunks.

### 4. AWS S3 (Raw Object Storage)

- **Configuration**: `S3_BUCKET` (using the same AWS credentials configured for Bedrock).
- **Purpose**: Stores original binary uploads (PDF, DOCX, DOC) and generates presigned GET download links valid for 15 minutes.
- **IAM Permissions Required**:
  - `s3:PutObject` on `arn:aws:s3:::{bucket}/documents/*`
  - `s3:GetObject` on `arn:aws:s3:::{bucket}/documents/*`
  - `s3:DeleteObject` on `arn:aws:s3:::{bucket}/documents/*`
  - `s3:HeadBucket` on `arn:aws:s3:::{bucket}`

---

## Health Probes and Readiness

The service exposes an unauthenticated health probe at `GET /health`.

```sh
curl localhost:8081/health
```

- Probes run sequentially against Pinecone, PostgreSQL, and configured S3 storage with a 5-second timeout.
- Returns HTTP 200 OK with `status: "ok"` when all dependencies respond.
- Returns HTTP 503 Service Unavailable with `status: "degraded"` if any dependency fails to respond.
- Use `GET /health` for container orchestrator readiness and liveness checks.

---

## Graceful Shutdown

The service handles graceful shutdown when receiving `SIGINT` or `SIGTERM`:

1. Stops accepting new inbound HTTP requests.
2. Drains active HTTP requests with a 15-second deadline.
3. Closes the ingestion worker queue and drains buffered ingest tasks with a 15-second deadline.
4. Closes active Pinecone index client connections.
5. Closes the PostgreSQL connection pool.

Ensure container orchestrators configure a termination grace period of at least 35 seconds to allow HTTP requests, worker drain and the bounded five-second failure-status update to settle before sending `SIGKILL`.

---

## Service Token Rotation

The `SERVICE_TOKEN` environment variable defines the secret expected in the `X-Service-Token` header. To rotate the token in a production deployment without downtime:

1. **Deploy calling services with dual-token support or phased updating**: Update the calling application to present the new token value, or stage the rotation in coordinated steps.
2. **Update the microservice environment**: Update the `SERVICE_TOKEN` environment variable on the microservice instance.
3. **Restart the microservice**: Perform a rolling restart of the microservice. In-flight requests presenting the existing token complete within the graceful shutdown window, while subsequent requests use the new token.
4. **Verify operations**: Send a probe request with the new token to `POST /query` or `GET /documents` to confirm authentication succeeds.
