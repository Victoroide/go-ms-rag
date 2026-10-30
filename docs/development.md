# Development

For contribution guidelines, environment setup, Conventional Commits formatting, and pull request procedures, refer to CONTRIBUTING.md. For vulnerability reporting procedures, refer to SECURITY.md.

---

## Repository Layout

```text
cmd/
  server/               Application entry point, dependency wiring, and graceful shutdown
internal/
  api/                  HTTP router, route registration, and middleware chaining
    handler/            HTTP handlers (document, file, health, ingest, json, query)
    middleware/         Structured JSON logging, panic recovery, and service token authentication
  chunker/              Sliding-window text chunker and SHA-256 content fingerprinting
  cleanup/              Multi-store document deletion coordinator (Pinecone, S3, PostgreSQL)
  config/               Environment variable loading, default fallbacks, and validation boundaries
  domain/               Domain types, shared interfaces, and default document label definitions
  embedder/             Bedrock Titan Embed V2 provider and offline fake embedder
  extractor/            Text extraction adapters for PDF, DOCX, DOC, and plain text
  llm/                  Bedrock Converse client, model routing matrix, and offline fake converser
  pipeline/             Ingestion stages (chunk, embed, persist) and query stages (decompose, route, retrieve, synthesize)
  retriever/            Vector retrieval coordinator, LOCAL/GLOBAL mode decision tree, and fallback logic
  store/
    memory/             In-memory thread-safe vector store used in offline fake mode
    pinecone/           Pinecone vector store driver and index connection pooling
    postgres/           PostgreSQL client, connection pool, and embedded SQL migrations (001–006)
    s3/                 AWS S3 client for raw object storage and presigned URL generation
  worker/               PostgreSQL ingestion leases, parallel processors, retry and shutdown handling
scripts/
  cleanup/              CLI utility to delete specified document UUIDs from all stores
  langeval/             Evaluation harness for response language compliance, latency, and chunk counts
  simulate/             End-to-end API simulation runner testing full document lifecycles
  validate/             Retrieval validation script verifying query modes and scoring
test/
  integration/          Integration tests using Testcontainers for isolated PostgreSQL testing
  smoke/                Live smoke test suite targeting running services (opt-in via RUN_SMOKE=1)
  unit/                 Unit tests covering chunking, extraction, fake mode, retriever, and router logic
```

---

## Test Suites

The test suite is structured into three tiers:

Host targets mirror the full validation path:

```sh
make vet
make lint
make test
make ci
```

`make ci` runs vet, lint and the full race suite in order, then builds the runtime image. `docker build --target test .` runs the short suite without requiring a nested Docker daemon.

### 1. Unit Tests

Unit tests execute without external dependencies, testing pure domain functions, data parsing, and mock-backed pipeline logic.

```sh
go test ./test/unit/... -count=1 -v
```

Scope of unit tests:
- **Chunker (`chunker_test.go`)**: Validates word window boundaries, token approximation formulas, HTML sanitization, and SHA-256 fingerprint generation.
- **Extractor (`extractor_test.go`)**: Validates text extraction from synthetic PDF, DOCX, DOC, and plain text buffers, including XML expansion limits and context cancellations.
- **DOC (`internal/extractor/doc_test.go`)**: Covers Word 97–2003 piece tables, Windows-1252 and UTF-16 text, nested field codes, corrupt structures, encrypted and older files, and decoded-text limits.
- **Fake Mode (`fakemode_test.go`)**: Verifies deterministic in-memory vector storage, mock embeddings, and fake converser outputs.
- **Named Entities (`named_entity_test.go`)**: Tests proper noun phrase extraction, stop-word filtering, and Unicode capitalization handling.
- **Retriever (`retriever_test.go`)**: Verifies LOCAL vs. GLOBAL mode determination, similarity score thresholding, and fallback search triggering when fewer than 3 chunks are returned.
- **Router (`router_test.go`)**: Validates model selection decisions across task types, context token thresholds, and synthesis modes.

### 2. Integration Tests

Integration tests verify database persistence, transactional rollbacks, schema migrations, and pipeline execution. Tests use Testcontainers to spin up isolated PostgreSQL containers automatically during execution.

```sh
go test -race -count=1 ./test/integration/...
```

Docker must be running locally to permit Testcontainers to manage the ephemeral PostgreSQL database instance. Database-backed tests skip under `go test -short` with an explicit daemon requirement message; CI runs the full suite without `-short`. The offline query test ingests through HTTP, polls document status until completion, and verifies cited evidence and document restrictions through the router.

### 3. Smoke Tests

Smoke tests run against live, deployed instances and external cloud services (AWS Bedrock, Pinecone, PostgreSQL, and S3). Smoke tests are excluded by default to avoid unexpected cloud consumption or accidental mutation of configured datasets. Accepted documents already have persisted metadata; bounded deferred deletion also removes asynchronous ingestion jobs.

To opt in to smoke testing against a configured service:

```sh
RUN_SMOKE=1 go test ./test/smoke/... -count=1 -v -timeout 120s
```

---

## Operational Scripts

The `scripts/` directory contains standalone administrative and evaluation tools:

### Document Cleanup (`scripts/cleanup`)

Accepts one or more document UUIDs as command-line arguments and removes their records from Pinecone, S3, and PostgreSQL using one shared set of clients:

```sh
make cleanup IDS="<document-uuid-1> <document-uuid-2>"
```

### Language Evaluation (`scripts/langeval`)

Measures whether generated answers match the language of the input question (e.g., English or Spanish) across a fixed evaluation query set, recording token usage and latency:

```sh
go run ./scripts/langeval
go run ./scripts/langeval -tag baseline
go run ./scripts/langeval -tag after
go run ./scripts/langeval -compare baseline,after
```

### End-to-End Simulation (`scripts/simulate`)

Simulates a sequential series of client interactions against a running instance on port 8081, verifying health checks, JSON ingestion, duplicate detection, vector query modes, and document deletion:

```sh
go run ./scripts/simulate/main.go
```

### Retrieval Validation (`scripts/validate`)

Dispatches predefined queries to a running instance to verify that routing logic correctly assigns LOCAL or GLOBAL retrieval modes and returns non-empty result sets:

```sh
go run ./scripts/validate/main.go
```

---

## Offline Fake Mode Internals

Setting `GO_RAG_FAKE_MODE=true` allows the entire microservice and its HTTP endpoints to run locally without connecting to AWS Bedrock, Pinecone, or S3.

### Implementation Architecture

When `FakeMode` is active in `cmd/server/main.go`:
1. **Vector Store**: Instantiates `internal/store/memory.FakeVectorStore`, which stores vector embeddings in a thread-safe Go map (`sync.RWMutex`). Vector distance calculations use cosine similarity computed in memory.
2. **Embedding Provider**: Instantiates `internal/embedder.FakeEmbedder`, generating deterministic 1024-dimensional float arrays based on hashing input texts.
3. **LLM Converser**: Instantiates `internal/llm.FakeConverser`, returning structured responses without invoking external APIs.
4. **Object Storage**: S3 client initialization is skipped. Multipart file ingestion extracts text directly in memory and continues through chunking and indexing without storing the raw binary.
5. **Relational Storage**: PostgreSQL is still utilized via `DATABASE_URL` for real schema migrations (001–006), metadata storage, and duplicate fingerprint tracking.

Durable ingestion tests use disposable PostgreSQL databases and the offline vector store to cover restart recovery, terminal failures, deletion during processing, NUL handling and source upload failures. Lease and atomic acceptance tests live in `internal/store/postgres/jobs_test.go`; the short suite skips database-backed tests.

The offline vector store enforces the hosted index's per-record limits: flat metadata values must be strings, numbers, booleans or lists of strings, serialized vector metadata must fit within 40,960 bytes, and vector identifiers must fit within 512 bytes. The metadata size includes chunk text and the required fields sent by the hosted adapter. The first stored embedding fixes the index dimension across all namespaces; later embeddings must match. Filtered queries accept at most 10,000 results. Upserts have no per-call batch limit because the hosted adapter batches requests. Offline writes are immediately consistent; the hosted index is eventually consistent.
