# Configuration

Configuration parameters are loaded exclusively from environment variables at startup via `internal/config/config.go`. The service fails fast during startup if required variables are missing if numerical boundaries are violated, or if numeric and boolean values cannot be parsed. Malformed values are errors rather than silent fallbacks.

## Environment Variables

The table below lists every environment variable parsed by the service:

| Variable | Type | Default | Constraints | Description |
|---|---|---|---|---|
| `SERVER_PORT` | string | `"8081"` | 1 to 65535 | TCP port on which the HTTP server listens. |
| `AWS_REGION` | string | `""` | Valid AWS region identifier | AWS region used for AWS Bedrock API calls and S3 object storage. Required when `GO_RAG_FAKE_MODE=false`. |
| `AWS_ACCESS_KEY_ID` | string | `""` | Non-empty string | AWS IAM access key ID. Required when `GO_RAG_FAKE_MODE=false`. |
| `AWS_SECRET_ACCESS_KEY` | string | `""` | Non-empty string | AWS IAM secret access key. Required when `GO_RAG_FAKE_MODE=false`. |
| `BEDROCK_EMBED_MODEL` | string | `"amazon.titan-embed-text-v2:0"` | Non-empty string | Bedrock model identifier used for generating text embeddings. |
| `BEDROCK_PRIMARY_MODEL` | string | `"amazon.nova-micro-v1:0"` | Non-empty string | Bedrock model identifier for low-latency reasoning and fast answer synthesis. |
| `BEDROCK_FALLBACK_MODEL` | string | `"us.meta.llama4-maverick-17b-instruct-v1:0"` | Non-empty string | Bedrock model identifier for answer synthesis and fallback routing. |
| `PINECONE_API_KEY` | string | `""` | Non-empty string | API key for authenticating with Pinecone vector indices. Required when `GO_RAG_FAKE_MODE=false`. |
| `PINECONE_INDEX_NAME` | string | `"go-ms-rag"` | Non-empty string | Name of the target Pinecone index. |
| `PINECONE_ENVIRONMENT` | string | `""` | String | Pinecone cloud environment identifier. |
| `DATABASE_URL` | string | `""` | Valid PostgreSQL connection string | PostgreSQL connection string for document metadata and chunk storage. Always required in all modes. Defaults to 16 pooled connections and a 30-second statement timeout; override with `pool_max_conns` and `statement_timeout`. |
| `WORKER_QUEUE_SIZE` | integer | `100` | 1 to 1000 | In-memory buffered channel capacity for the background ingestion worker. |
| `EMBED_CONCURRENCY` | integer | `10` | 1 to 50 | Maximum number of concurrent embedding requests dispatched to AWS Bedrock. |
| `LOCAL_RETRIEVAL_THRESHOLD` | float64 | `0.70` | 0.0 to 1.0 | Cosine similarity threshold for routing single-need queries to LOCAL retrieval mode. |
| `MAX_CHUNKS_PER_QUERY` | integer | `10` | 1 to 100 | Default and maximum number of chunks returned by the retriever per query. |
| `CHUNK_TOKEN_SIZE` | integer | `512` | 1 to 8192 | Approximate target token size per text chunk in the sliding window chunker. |
| `CHUNK_OVERLAP` | integer | `64` | >= 0 and < `CHUNK_TOKEN_SIZE` | Number of overlapping tokens retained between consecutive text chunks. |
| `EMBEDDING_DIMENSIONS` | integer | `1024` | 256, 512, or 1024 | Output vector dimensions requested from Bedrock Titan Embed V2. |
| `S3_BUCKET` | string | `"ms-go-rag"` | String | S3 bucket name used for storing raw binary uploads. If empty or initialization fails, source file storage is disabled while text ingestion continues. |
| `MAX_UPLOAD_BYTES` | int64 | `20971520` (20 MiB) | 1 to 104857600 (100 MiB) | Maximum accepted request payload size for multipart file uploads in bytes. |
| `SYNTHESIS_MODEL` | string | `"fallback"` | `"primary"`, `"fallback"`, or `"hybrid"` | Model routing policy for query answer synthesis: `"fallback"` uses Llama 4 Maverick, `"primary"` uses Nova Micro, and `"hybrid"` dynamically selects between models based on query complexity. |
| `GO_RAG_FAKE_MODE` | boolean | `false` | Boolean (`true`, `false`, `1`, `0`) | Runs the service in offline fake mode using an in-memory vector store and mock model generators without AWS or Pinecone credentials. Never enable in production deployments. |
| `SERVICE_TOKEN` | string | `""` | Non-empty string in real mode | Shared secret token expected in the `X-Service-Token` header on protected endpoints. Required when `GO_RAG_FAKE_MODE=false`. |

---

## Schema Migrations

Database schema migrations are SQL files named with a three-digit version and a descriptive suffix, such as `006_add_index.sql`. Every file under `internal/store/postgres/migrations` matching `NNN_name.sql` is embedded in the binary automatically. Versions must be unique and are applied in ascending order by `internal/store/postgres/migrate.go`.

On startup, the runner acquires a PostgreSQL transaction-scoped advisory lock and applies missing versions in one transaction. The `schema_migrations` table records each version, filename, SHA-256 checksum and application timestamp. Concurrent replicas wait for the lock before checking this table. Subsequent startups verify checksums and skip applied SQL, avoiding table locks on document data when no migration is pending. Never edit an applied migration; add a new numbered file instead. A checksum mismatch stops startup. Existing databases without migration history apply the original idempotent files once and record them without removing stored documents.

Application connections default to `pool_max_conns=16` and `statement_timeout=30000` (milliseconds). Explicit settings in `DATABASE_URL` override these defaults, including `statement_timeout=0` to disable the timeout. Both URI query parameters and keyword connection strings are supported; for example, `host=localhost dbname=rag pool_max_conns=24 statement_timeout=10000` sets 24 connections and a 10-second statement timeout. The migration transaction disables statement and lock timeouts while waiting for the advisory lock, then uses a 15-second lock timeout when applying SQL. Transaction-local settings do not change application connection settings after commit or rollback.

| File | Description |
|---|---|
| `001_init.sql` | Enables the `pgcrypto` extension. Creates the `documents` table, the `chunks` table with foreign key cascading deletion (`ON DELETE CASCADE`), and the `document_fingerprints` deduplication table with indexing. |
| `002_add_file_storage.sql` | Adds nullable columns `file_name`, `file_type`, and `s3_key` to `documents` to track raw uploaded files stored in S3. |
| `003_add_content_preview.sql` | Adds the `content_preview` column to `documents`, backfills existing rows with the first 120 characters of `content`, and creates a functional index on `LOWER(content_preview)`. |
| `004_add_doc_type.sql` | Adds the `doc_type` column to `documents` and backfills values from `metadata->>'doc_type'`. |
| `005_document_default.sql` | Sets the default column value for `doc_type` in `documents` to `'document'`. |
