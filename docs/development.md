# Development

## Running Tests

### Unit Tests
```bash
go test ./test/unit/... -count=1 -v
```

### Integration Tests
```bash
go test ./test/integration/... -count=1 -v
```

### Smoke Tests (requires running server)
```bash
RUN_SMOKE=1 go test ./test/smoke/... -count=1 -v -timeout 120s
```

### All Tests
```bash
go test ./... -count=1 -timeout 120s
```

### Race Detection
```bash
CGO_ENABLED=1 go test ./... -count=1 -race
```

## Build Gate

Run the full build gate before committing:
```bash
go mod tidy
go build ./...
go vet ./...
golangci-lint run --config=.golangci.yml
go test ./... -count=1 -timeout 120s
```

## File Extractor

The `internal/extractor` package provides text extraction from multiple file formats:

| Format | Extension | Library | Notes |
|---|---|---|---|
| PDF | `.pdf` | `github.com/ledongthuc/pdf` | Page-by-page plain text extraction |
| DOCX | `.docx` | `archive/zip` + `encoding/xml` | Reads `word/document.xml`, walks `<w:p>` and `<w:t>` elements |
| DOC | `.doc` | `github.com/richardlehane/mscfb` | Parses OLE2 compound file, extracts printable text from WordDocument stream |
| Text | — | stdlib | Passthrough, returns trimmed input |

All extractors:
- Return an error if extracted text is shorter than 10 characters.
- Do not panic. All errors are wrapped with context.
- Do not spawn goroutines. Concurrency is managed at the pipeline level.

### Adding a New Extractor

1. Create `internal/extractor/<format>.go` implementing the `Extractor` interface.
2. Add the `FileType` constant to `extractor.go`.
3. Register it in `DetectFileType` and `New` factory.
4. Add unit tests in `test/unit/extractor_test.go`.

## S3 Storage

The `internal/store/s3` package wraps the AWS SDK v2 S3 client.

- **Upload**: `PutObject` to `documents/{doc_type}/{doc_id}/{filename}`.
- **PresignGet**: Generates a presigned GET URL valid for 15 minutes.
- **Ping**: `HeadBucket` to verify connectivity.

S3 initialization is non-fatal. If S3 fails to initialize, the service runs without file upload support. File uploads via `POST /ingest` multipart still extract text and ingest it; only the S3 upload step is skipped.

## Database Schema

### documents table

| Column | Type | Nullable | Description |
|---|---|---|---|
| `id` | UUID | no | Primary key |
| `content` | TEXT | no | Full document text |
| `metadata` | JSONB | no | Arbitrary metadata |
| `status` | TEXT | no | `processing`, `completed`, `failed` |
| `chunk_count` | INTEGER | no | Number of chunks |
| `file_name` | TEXT | yes | Original uploaded filename |
| `file_type` | TEXT | yes | File type (pdf, docx, doc) |
| `s3_key` | TEXT | yes | S3 object key |
| `created_at` | TIMESTAMPTZ | no | Creation timestamp |

### chunks table

| Column | Type | Description |
|---|---|---|
| `id` | UUID | Primary key |
| `document_id` | UUID | FK to documents |
| `chunk_index` | INTEGER | Chunk position |
| `text` | TEXT | Chunk text |
| `token_count` | INTEGER | Approximate token count |

### document_fingerprints table

| Column | Type | Description |
|---|---|---|
| `fingerprint` | TEXT | SHA-256 hex of content |
| `document_id` | UUID | FK to documents |

## Scripts

Operational tools in `scripts/` are not part of the service binary. They share the same Go module but are excluded from the Docker build via `.dockerignore`.

```bash
make cleanup   # deletes all data from Pinecone and Neon
make validate  # runs the 14-query business validation
```

Run these only against a known environment. Never run cleanup against production without a backup.
