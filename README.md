# go-ms-rag

RAG microservice for FICCT Jobs. A small Go HTTP service that owns the
retrieval-augmented-generation pipeline: it ingests documents (CVs, vacancy text),
embeds and stores them, and answers semantic queries grounded in the stored corpus.

## Routes

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/ingest` | service token | Accept JSON or multipart; dedupe by fingerprint; enqueue ingest |
| POST | `/query` | service token | Run the retrieval pipeline and synthesize an answer |
| DELETE | `/documents/{id}` | service token | Delete vectors (namespace + default), rows, S3, chunks |
| GET | `/documents/{id}/file` | public | Download the original binary from S3 |
| GET | `/health` | public | Ping Pinecone + Neon + S3 |

## Pipeline

- **Ingest**: extract text (pdf/docx/doc/text) → chunk (sliding window, SHA-256
  fingerprint) → embed (Bedrock Titan v2, 1024-dim) → upsert to Pinecone (per-document
  namespace + default) → persist documents/chunks in Postgres.
- **Query**: decompose (Nova Micro, single vs multi sub-query) → route LOCAL/GLOBAL by
  top similarity → retrieve from Pinecone (filtered by `doc_type`) → synthesize
  (Llama 4 Maverick via Bedrock Converse).

## Configuration (env)

| Var | Default | Meaning |
|---|---|---|
| `SERVICE_TOKEN` | `""` | When set, `/ingest`, `/query`, `DELETE /documents/{id}` require a matching `X-Service-Token` (401 otherwise). `/health` and the file GET stay public. **Set in production.** |
| `GO_RAG_FAKE_MODE` | `false` | Run fully offline: deterministic embeddings/LLM, in-memory store, no AWS/Pinecone. For local dev and unit/integration tests. |
| AWS / Pinecone / Neon / S3 | — | Required in real mode (not in fake mode). |

The caller (the Django backend) sends `X-Service-Token = GO_RAG_SERVICE_TOKEN`, which
must equal this service's `SERVICE_TOKEN`. To enable or rotate the token without a
broken window, set it on the backend first, then here (see the FICCT Jobs backend
`docs/railway-deployment.md`).

## Build & test

```bash
go build ./...
go vet ./...
gofmt -l .
go test ./test/unit/...
```

## Deployment

Railway service `go-ms-rag` in project `ficct-jobs-project` (auto-deploy on push to
`main`). Public domain `https://dev-go-ms-rag.jobs.ficct.com`; `/health` is the
healthcheck path.
