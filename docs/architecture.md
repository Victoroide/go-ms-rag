# Architecture

## Overview

go-ms-rag is a retrieval-augmented generation (RAG) microservice written in Go. It ingests documents (text, PDF, DOCX, DOC), splits them into token-bounded chunks, generates vector embeddings via AWS Bedrock, stores vectors in Pinecone, persists metadata in PostgreSQL (Neon), and optionally uploads source files to AWS S3.

## Component Diagram

```
┌─────────────────────────────────────────────────────────┐
│                      HTTP API                           │
│  POST /ingest (JSON or multipart)                       │
│  POST /query                                            │
│  GET  /health                                           │
│  GET  /documents/{doc_id}/file                          │
└────────────┬────────────────────────────────────────────┘
             │
     ┌───────▼────────┐
     │  Worker Queue   │  buffered channel, single consumer
     └───────┬────────┘
             │
     ┌───────▼────────────────────────┐
     │     Ingestion Pipeline         │
     │  ChunkStage → EmbedStage →     │
     │  PersistStage                  │
     └──┬──────────┬─────────────┬───┘
        │          │             │
   ┌────▼───┐ ┌───▼────┐  ┌────▼─────┐
   │Pinecone│ │Postgres │  │   S3     │
   │(vectors)│ │(metadata)│  │(files)   │
   └────────┘ └─────────┘  └──────────┘
```

## Package Layout

| Package | Responsibility |
|---|---|
| `cmd/server` | Entry point, dependency wiring, HTTP server lifecycle |
| `internal/api` | Router, middleware, HTTP handlers |
| `internal/chunker` | Token-bounded sliding-window text chunking, fingerprinting |
| `internal/config` | Environment-based configuration loading and validation |
| `internal/domain` | Core types and interfaces shared across packages |
| `internal/embedder` | AWS Bedrock Titan embedding provider |
| `internal/extractor` | Text extraction from PDF, DOCX, DOC files |
| `internal/llm` | Bedrock LLM client and ambassador (primary/fallback) |
| `internal/pipeline` | Ingestion and retrieval pipeline stages |
| `internal/retriever` | Adaptive local/global retrieval with query decomposition |
| `internal/store/pinecone` | Pinecone vector store client and driver |
| `internal/store/postgres` | PostgreSQL metadata store, migrations |
| `internal/store/s3` | AWS S3 file storage client |
| `internal/worker` | Async ingest queue with buffered channel |

## Data Flow

### Ingestion (JSON)

1. `POST /ingest` receives JSON with `content` field.
2. Handler computes fingerprint and checks for duplicates.
3. Task is enqueued to the worker queue.
4. Worker runs the ingestion pipeline: Chunk → Embed → Persist.
5. PersistStage upserts vectors to Pinecone (document namespace + default namespace), inserts document and chunk metadata to Postgres, and stores the fingerprint.
6. Document status is updated to `completed` or `failed`.

### Ingestion (File Upload)

1. `POST /ingest` receives `multipart/form-data` with a `file` field.
2. Handler validates file type (.pdf, .docx, .doc) and size (≤20MB).
3. Text is extracted from the file using the appropriate extractor.
4. File is uploaded to S3 at `documents/{doc_type}/{doc_id}/{filename}`.
5. Extracted text follows the same pipeline as JSON ingestion.
6. File metadata (file_name, file_type, s3_key) is stored in Postgres.

### File Retrieval

1. `GET /documents/{doc_id}/file` looks up the S3 key in Postgres.
2. If a file exists, a presigned GET URL (15-minute expiry) is returned.

### Query

1. `POST /query` performs named entity detection, decomposes the query, routes to local or global retrieval, retrieves relevant chunks, and synthesizes an answer via the LLM.

## Named Entity Detection

The system dynamically detects named entities (people, companies) from document previews to optimize query routing:

- **Extraction**: For each document, the first 60 characters are stored in `content_preview`. The `ExtractNamedEntity` function identifies capitalized names and company names (e.g., "Laura Benitez", "DataFlow").
- **Caching**: `FetchNamedEntitiesFromDB` loads all entities from Postgres with a 60-second TTL cache to avoid database round-trips on every query.
- **Routing**: If a query contains a named entity, it skips LLM decomposition and routes directly to LOCAL mode, ensuring precise document retrieval.

## Data Model

### Documents Table
```sql
CREATE TABLE documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content         TEXT NOT NULL,
    content_preview TEXT,                    -- First 60 chars for entity extraction
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    status          TEXT NOT NULL DEFAULT 'processing',
    chunk_count     INTEGER NOT NULL DEFAULT 0,
    file_name       TEXT,                    -- For uploaded files
    file_type       TEXT,                    -- MIME type of uploaded file
    s3_key          TEXT,                    -- S3 object key
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### Chunks Table
```sql
CREATE TABLE chunks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    text        TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

## Concurrency Model

- **Worker Queue**: Single-consumer buffered channel processes ingestions sequentially to avoid resource contention.
- **S3 Upload**: Uses `errgroup` to concurrently upload file to S3 while generating embeddings, reducing total ingestion latency.
- **Embedding Generation**: Batches requests to Bedrock with configurable concurrency (`EMBED_CONCURRENCY`).
- **Query Processing**: Each request runs in its own goroutine; named entity cache uses `sync.RWMutex` for concurrent access.
