# Development

## Prerequisites

- Go 1.26+
- Docker 27+ (for containerized builds/tests)
- golangci-lint (install: `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`)
- A `.env` file with valid credentials (see [configuration.md](configuration.md))

## Local Setup

1. Clone the repository.
2. Copy `.env.example` to `.env` and fill in all required credentials.
3. Build and run directly:
   ```bash
   go build -o server ./cmd/server
   ./server
   ```
4. Or via Docker Compose:
   ```bash
   docker compose up --build -d
   ```

The server starts on the port specified by `SERVER_PORT` (default `8081`).

## Running Tests

### All Tests with Race Detection

```bash
CGO_ENABLED=1 go test ./... -count=1 -race
```

### Unit Tests

```bash
go test ./test/unit/... -count=1 -v
```

Covers:
- **Retriever logic** (`retriever_test.go`): `DetermineMode`, `DetermineModeWithQuery`, `Retrieve` with mock VectorStore and Embedder. Verifies fallback from GLOBAL with < 3 chunks.
- **LLM routing** (`router_test.go`): Verifies model selection based on task type, context tokens, and failure reasons. Tests context limit rerouting, synthesis-always-maverick, and structured output fallback.

### Integration Tests

```bash
go test ./test/integration/... -count=1 -v
```

Covers:
- **Ingest handler** (`ingest_test.go`): JSON ingest, multipart file ingest (DOCX), deduplication via fingerprint, file size limit enforcement. Uses mock ChunkStore, VectorStore, and Embedder. Tests that the worker processes tasks and the pipeline runs end-to-end in-process.

### Smoke Tests

```bash
RUN_SMOKE=1 go test ./test/smoke/... -count=1 -v -timeout 120s
```

Requires a running server (via Docker Compose or local). Covers:
- Health check endpoint.
- DOCX file ingest (creates a real document, waits for completion, verifies in Postgres).
- Unsupported file type rejection (.txt).
- Large file upload rejection.
- Automatic cleanup: smoke tests delete any documents they create via `cleanup.CleanupDocument`.

## Linting

```bash
golangci-lint run --config=.golangci.yml
```

The `.golangci.yml` configuration enables linters including `govet`, `staticcheck`, `errcheck`, `gosimple`, `ineffassign`, `unused`, `gocritic`, `misspell`, `gofmt`, and `goimports`. Test files in `test/` and `scripts/` are included in the lint scope.

## Build Gate

Run the full quality gate before any merge:

```bash
go mod tidy
go build ./...
go vet ./...
golangci-lint run --config=.golangci.yml
CGO_ENABLED=1 go test ./... -count=1 -race
```

All four commands must exit 0.

## Scripts (scripts/ directory)

Scripts are standalone Go programs in `scripts/`. They share the same `go.mod` but are excluded from the Docker image via `.dockerignore`. Each script loads `.env` for database and service credentials.

### scripts/cleanup

Deletes all test data from Pinecone and Postgres. Iterates over documents in Postgres, calls `cleanup.CleanupDocument` for each, which:
1. Deletes vectors from Pinecone (candidate namespace, default namespace, document namespace).
2. Deletes from Postgres (chunks, fingerprints, documents) via batch query.

```bash
go run ./scripts/cleanup/main.go
```

### scripts/validate

Runs business validation queries against a live server. Executes predefined queries and checks response structure.

```bash
go run ./scripts/validate/main.go
```

### scripts/simulate

Runs all 14 Django call pattern simulation scenarios sequentially. Stops on the first failure and reports a summary. Scenarios include:

| # | Scenario | Verifies |
|---|---|---|
| 1 | Health check | `GET /health` returns 200, all services `"ok"` |
| 2 | Ingest CV (JSON, doc_type=candidate) | 202/200, valid UUID returned |
| 3 | Ingest job (JSON, doc_type=job) | 202/200, valid UUID returned |
| -- | Postgres verification | Documents exist with correct `status` and `doc_type` |
| 4 | Deduplication check | Same content returns `"already_ingested"` |
| 5 | Query filter_doc_type=candidate | Only candidate chunks returned |
| 6 | Query filter_doc_type=job | Only job chunks returned |
| 7 | Semantic ranking (candidate) | Chunks sorted by score descending, all doc_type=candidate |
| 8 | Vacancy recommendations (job) | Job document appears in scored_chunks |
| 9 | Skill gap analysis | chunks_used >= 2, non-empty answer |
| 10 | File retrieval URL | 404 for JSON-ingested doc (no S3 file) |
| 11 | Delete document | 200, verified gone from file endpoint and query results |
| 12 | Delete non-existent document | 404 |
| 13 | Invalid file type rejection | 415 for .csv upload |
| 14 | Health reflects real state | 200, all services still `"ok"` |

```bash
go run ./scripts/simulate/main.go
```

## Adding a New Document Format

1. Create `internal/extractor/<format>.go` with a struct implementing the `Extractor` interface:
   ```go
   type Extractor interface {
       Extract(ctx context.Context, r io.Reader) (string, error)
       Type() FileType
   }
   ```
2. Add a `FileType` constant in `internal/extractor/extractor.go` (e.g. `FileTypeXLSX FileType = "xlsx"`).
3. Add the extension to the `switch` in `DetectFileType`.
4. Add the extractor to the `switch` in `New`.
5. Add the MIME type to `ContentTypeFromFileType`.
6. Add unit tests.

## Adding a New Endpoint

1. Create a handler file in `internal/api/handler/` (e.g. `stats.go`).
2. Define request/response structs with JSON tags.
3. Write the handler function following the pattern: `func StatsHandler(deps...) http.HandlerFunc`.
4. Register the route in `internal/api/router.go` -> `NewRouter` function.
5. If the handler needs a new store method, add it to the appropriate interface in `internal/domain/interfaces.go` and implement it in the store package.
6. Update `RouterDeps` struct if new dependencies are needed.
7. Wire the dependency in `cmd/server/main.go`.

## Package Dependency Rules

- **`internal/domain`** has zero internal imports. All other packages may import it.
- **Store packages** (`pinecone`, `postgres`, `s3`) import only `domain` and `config`.
- **`internal/pipeline`** imports `domain`, `chunker`, `retriever`, `llm`, `config`, `postgres` (for named entity cache only).
- **`internal/retriever`** imports only `domain`.
- **`internal/api/handler`** imports `domain`, `worker`, store packages, `pipeline`, `retriever`, `llm`, `config`.
- **`internal/worker`** imports `domain`, `pipeline`, `chunker`.
- **`cmd/server`** imports everything to wire dependencies.
- **`internal/cleanup`** is a standalone utility; imports `config`, `pinecone`, `postgres`.
