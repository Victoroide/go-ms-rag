# Configuration

All configuration is loaded from environment variables at startup via `internal/config/config.go`. Required variables cause a fatal startup error if missing.

## Environment Variables

### Server

| Variable | Type | Required | Default | Purpose |
|---|---|---|---|---|
| `SERVER_PORT` | `string` | no | `8081` | HTTP server listen port |

### Postgres

| Variable | Type | Required | Default | Purpose |
|---|---|---|---|---|
| `DATABASE_URL` | `string` | yes | -- | PostgreSQL connection string (e.g. Neon) |

### Pinecone

| Variable | Type | Required | Default | Purpose |
|---|---|---|---|---|
| `PINECONE_API_KEY` | `string` | yes | -- | Pinecone API key |
| `PINECONE_INDEX_NAME` | `string` | no | `go-ms-rag` | Pinecone index name |
| `PINECONE_ENVIRONMENT` | `string` | no | -- | Pinecone environment (not currently used by SDK v1.1.1 but stored in Config) |

### Bedrock (AWS)

| Variable | Type | Required | Default | Purpose |
|---|---|---|---|---|
| `AWS_REGION` | `string` | yes | -- | AWS region for Bedrock and S3 |
| `AWS_ACCESS_KEY_ID` | `string` | yes | -- | AWS IAM access key |
| `AWS_SECRET_ACCESS_KEY` | `string` | yes | -- | AWS IAM secret key |
| `BEDROCK_EMBED_MODEL` | `string` | no | `amazon.titan-embed-text-v2:0` | Bedrock embedding model ID |
| `BEDROCK_PRIMARY_MODEL` | `string` | no | `amazon.nova-micro-v1:0` | Primary LLM model for query decomposition and extraction |
| `BEDROCK_FALLBACK_MODEL` | `string` | no | `us.meta.llama4-maverick-17b-instruct-v1:0` | Fallback LLM model; always used for synthesis |

### S3

| Variable | Type | Required | Default | Purpose |
|---|---|---|---|---|
| `S3_BUCKET` | `string` | no | `ms-go-rag` | S3 bucket name for file uploads |
| `MAX_UPLOAD_BYTES` | `int64` | no | `20971520` | Maximum file upload size in bytes (20 MB) |

### Retrieval

| Variable | Type | Required | Default | Purpose |
|---|---|---|---|---|
| `LOCAL_RETRIEVAL_THRESHOLD` | `float64` | no | `0.70` | Cosine similarity threshold. Scores >= this value route to LOCAL mode (when sub_queries <= 1 and no named entity). Scores below route to GLOBAL. |
| `MAX_CHUNKS_PER_QUERY` | `int` | no | `10` | Maximum chunks returned per query. Also the default `topK` for Retriever. |

### Chunking & Embedding

| Variable | Type | Required | Default | Purpose |
|---|---|---|---|---|
| `CHUNK_TOKEN_SIZE` | `int` | no | `512` | Approximate tokens per chunk (words = tokens / 1.3) |
| `CHUNK_OVERLAP` | `int` | no | `64` | Token overlap between consecutive chunks |
| `EMBEDDING_DIMENSIONS` | `int` | no | `1024` | Embedding vector dimension passed to Titan Embed V2 |

### Worker

| Variable | Type | Required | Default | Purpose |
|---|---|---|---|---|
| `WORKER_QUEUE_SIZE` | `int` | no | `100` | Buffered channel capacity for ingest queue (valid: 1--1000) |
| `EMBED_CONCURRENCY` | `int` | no | `10` | Max concurrent Bedrock InvokeModel calls for embeddings (valid: 1--50) |

## Validation

Startup fails if:
- Any of `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `PINECONE_API_KEY`, `DATABASE_URL` is empty.
- `WORKER_QUEUE_SIZE` is outside 1--1000.
- `EMBED_CONCURRENCY` is outside 1--50.

## S3 Configuration

The S3 client uses the same AWS credentials as Bedrock. If `S3_BUCKET` is non-empty, the service initializes the S3 client at startup. If S3 initialization fails, the service continues to operate with file uploads disabled -- a warning is logged. File uploads via `POST /ingest` multipart still extract text and ingest it; only the S3 upload step is skipped.

S3 key structure: `documents/{doc_type}/{doc_id}/{filename}`

## Migrations

Migrations are embedded in the Go binary via `//go:embed` and applied automatically at startup in order. No external migration tool is needed.

| File | Description |
|---|---|
| `001_init.sql` | Creates `documents`, `chunks`, `document_fingerprints` tables with pgcrypto extension. Adds indexes on `chunks(document_id)` and `document_fingerprints(document_id)`. |
| `002_add_file_storage.sql` | Adds `file_name`, `file_type`, `s3_key` columns to `documents`. |
| `003_add_content_preview.sql` | Adds `content_preview` column to `documents`. Backfills with `LEFT(content, 120)`. Creates index on `LOWER(content_preview)`. |
| `004_add_doc_type.sql` | Adds `doc_type TEXT NOT NULL DEFAULT 'candidate'` column to `documents`. Backfills from `metadata->>'doc_type'` where present. |
