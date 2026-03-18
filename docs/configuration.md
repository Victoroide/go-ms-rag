# Configuration

All configuration is loaded from environment variables at startup. Required variables cause a fatal error if missing.

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `AWS_REGION` | yes | — | AWS region for Bedrock, S3 |
| `AWS_ACCESS_KEY_ID` | yes | — | AWS IAM access key |
| `AWS_SECRET_ACCESS_KEY` | yes | — | AWS IAM secret key |
| `PINECONE_API_KEY` | yes | — | Pinecone API key |
| `PINECONE_INDEX_NAME` | no | `go-ms-rag` | Pinecone index name |
| `PINECONE_ENVIRONMENT` | no | — | Pinecone environment |
| `DATABASE_URL` | yes | — | PostgreSQL connection string |
| `SERVER_PORT` | no | `8081` | HTTP server port |
| `BEDROCK_EMBED_MODEL` | no | `amazon.titan-embed-text-v2:0` | Bedrock embedding model ID |
| `BEDROCK_PRIMARY_MODEL` | no | `amazon.nova-micro-v1:0` | Primary LLM model |
| `BEDROCK_FALLBACK_MODEL` | no | `us.meta.llama4-maverick-17b-instruct-v1:0` | Fallback LLM model |
| `WORKER_QUEUE_SIZE` | no | `100` | Ingest queue capacity (1–1000) |
| `EMBED_CONCURRENCY` | no | `10` | Concurrent embedding requests (1–50) |
| `LOCAL_RETRIEVAL_THRESHOLD` | no | `0.70` | Similarity threshold for local retrieval |
| `MAX_CHUNKS_PER_QUERY` | no | `10` | Maximum chunks returned per query |
| `CHUNK_TOKEN_SIZE` | no | `512` | Approximate tokens per chunk |
| `CHUNK_OVERLAP` | no | `64` | Token overlap between consecutive chunks |
| `EMBEDDING_DIMENSIONS` | no | `1024` | Embedding vector dimension |
| `S3_BUCKET` | no | `ms-go-rag` | S3 bucket for file uploads |
| `MAX_UPLOAD_BYTES` | no | `20971520` | Maximum file upload size in bytes (20MB) |

## S3 Configuration

The S3 client uses the same AWS credentials as Bedrock. If `S3_BUCKET` is set, the service initializes the S3 client at startup. If S3 initialization fails, the service continues to operate with file uploads disabled — a warning is logged.

S3 key structure: `documents/{doc_type}/{doc_id}/{filename}`

## Database Migrations

Migrations are embedded in the binary and applied automatically at startup:
- `001_init.sql` — Creates `documents`, `chunks`, and `document_fingerprints` tables.
- `002_add_file_storage.sql` — Adds `file_name`, `file_type`, `s3_key` columns to `documents`.
- `003_add_content_preview.sql` — Adds `content_preview` column for named entity extraction.
