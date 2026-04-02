# Deployment

## Prerequisites

| Dependency | Version | Source |
|---|---|---|
| Go | 1.26+ | `go.mod` line 3 |
| Docker | 27+ | For containerized deployment |
| AWS account | -- | Bedrock (Titan Embed V2, Nova Micro, Llama4 Maverick) + S3 |
| Pinecone | -- | Serverless or pod-based index |
| PostgreSQL | -- | Neon recommended; any PG 14+ works |

Key Go module dependencies (from `go.mod`):

| Module | Version |
|---|---|
| `github.com/pinecone-io/go-pinecone` | `v1.1.1` |
| `github.com/jackc/pgx/v5` | `v5.8.0` |
| `github.com/aws/aws-sdk-go-v2` | `v1.41.4` |
| `github.com/aws/aws-sdk-go-v2/service/bedrockruntime` | `v1.49.0` |
| `github.com/aws/aws-sdk-go-v2/service/s3` | `v1.97.1` |
| `golang.org/x/sync` | `v0.19.0` |
| `google.golang.org/protobuf` | `v1.36.11` |

## Environment Setup

Create a `.env` file with all required and optional variables. See [configuration.md](configuration.md) for the full list.

Required variables (service will not start without these):

```
AWS_REGION=
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
PINECONE_API_KEY=
DATABASE_URL=
```

Do not commit secrets to version control. Use a `.env` file or a secrets manager.

## Docker Compose

The `docker-compose.yml` defines a single service:

```yaml
services:
  app:
    build:
      context: .
      target: runtime
    ports:
      - "8081:8081"
    env_file:
      - .env
    restart: unless-stopped
```

**`app` service:** Builds the Go binary in a multi-stage Docker build and runs it as the sole container. All external dependencies (Postgres, Pinecone, S3, Bedrock) are remote services accessed via environment variables. There is no local database container.

Build and run:

```bash
docker compose up --build -d
```

The Dockerfile has four stages:

| Stage | Base | Purpose |
|---|---|---|
| `base` | `golang:1.26-alpine` | Installs gcc, musl-dev, git. Downloads Go modules. |
| `test` | `base` | Runs `go vet ./...` and `CGO_ENABLED=1 go test ./... -count=1 -race`. |
| `lint` | `base` | Installs and runs `golangci-lint` with project config. |
| `builder` | `base` | Compiles binary: `CGO_ENABLED=0 go build -o /bin/server ./cmd/server`. |
| `runtime` | `alpine:3.21` | Copies binary + migration SQL files. Exposes port 8081. |

The `docker compose` target is `runtime`, which skips the `test` and `lint` stages. To run those in CI, target them explicitly.

## Database Migrations

Migrations are embedded in the Go binary via `//go:embed` and applied automatically at startup in `postgres.Client.RunMigrations`. No external migration tool is needed.

| Migration | What It Does |
|---|---|
| `001_init.sql` | Creates `pgcrypto` extension. Creates `documents` table (id, content, metadata, status, chunk_count, created_at). Creates `chunks` table (id, document_id FK CASCADE, chunk_index, text, token_count, created_at). Creates `document_fingerprints` table (fingerprint PK, document_id FK CASCADE, created_at). Adds indexes on `chunks(document_id)` and `document_fingerprints(document_id)`. |
| `002_add_file_storage.sql` | Adds `file_name TEXT`, `file_type TEXT`, `s3_key TEXT` columns to `documents`. |
| `003_add_content_preview.sql` | Adds `content_preview TEXT` column to `documents`. Backfills existing rows with `LEFT(content, 120)`. Creates index on `LOWER(content_preview)`. |
| `004_add_doc_type.sql` | Adds `doc_type TEXT NOT NULL DEFAULT 'candidate'` column to `documents`. Backfills from `metadata->>'doc_type'` where present. |

All migrations are idempotent (`IF NOT EXISTS` / `DO $$ ... END $$`).

## S3 Setup

Create an S3 bucket matching the `S3_BUCKET` environment variable (default: `ms-go-rag`).

### Required IAM Actions

| Action | Resource | Purpose |
|---|---|---|
| `s3:PutObject` | `arn:aws:s3:::{bucket}/documents/*` | File uploads during ingest |
| `s3:GetObject` | `arn:aws:s3:::{bucket}/documents/*` | Presigned URL generation for file retrieval |
| `s3:DeleteObject` | `arn:aws:s3:::{bucket}/documents/*` | File deletion via `DELETE /documents/{doc_id}` |
| `s3:HeadBucket` | `arn:aws:s3:::{bucket}` | Health check probe |

### IAM Policy

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"],
      "Resource": "arn:aws:s3:::your-bucket-name/documents/*"
    },
    {
      "Effect": "Allow",
      "Action": ["s3:HeadBucket"],
      "Resource": "arn:aws:s3:::your-bucket-name"
    }
  ]
}
```

### Bucket Configuration

- No public access required.
- Versioning optional (recommended for production).
- Default encryption (SSE-S3) recommended.
- No bucket policy or CORS needed for server-side operations. If clients download files via presigned URLs directly, ensure the bucket allows GET from expected origins.

## Pinecone Setup

| Setting | Value |
|---|---|
| Index name | Must match `PINECONE_INDEX_NAME` (default: `go-ms-rag`) |
| Dimension | `1024` (must match `EMBEDDING_DIMENSIONS`) |
| Metric | `cosine` |
| Type | Serverless or pod-based |

The service creates namespace-scoped connections on demand. Each document gets its own namespace (namespace = document UUID). All vectors are also written to the default (empty) namespace for global queries.

## First Run Verification

After starting the service, verify with:

```bash
curl http://localhost:8081/health
```

Expected response:

```json
{"status":"ok","pinecone":"ok","neon":"ok","s3":"ok"}
```

Each probe has a 5-second timeout. If any probe fails, `status` becomes `"degraded"` and the response code is `503`.

## Production Considerations

- **S3 presigned URL TTL:** Hardcoded to 15 minutes. Clients must request a fresh URL if the previous one expires.
- **Bedrock rate limits:** The service retries on `ThrottlingException` with exponential backoff (max 5 retries, max 30s delay). High query volume may exceed Bedrock quotas; request a quota increase for `InvokeModel` and `Converse` APIs.
- **Named entity cache TTL:** 60 seconds. After a document is deleted, its named entity may still be cached for up to 60 seconds.
- **Worker queue:** Single-consumer, sequential processing. If ingest throughput is a bottleneck, increase `WORKER_QUEUE_SIZE` and consider scaling horizontally.
- **Server timeouts:** Read timeout 30s, write timeout 120s. Synthesis calls to Llama4 Maverick can take several seconds; ensure load balancer timeouts are >= 120s.
- **Graceful shutdown:** The service listens for SIGINT/SIGTERM, shuts down the worker (waits for in-flight task), and drains HTTP connections with a 15-second deadline.
