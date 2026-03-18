# API Reference

## Endpoints

### GET /health

Returns service health status including connectivity to Pinecone, PostgreSQL (Neon), and S3.

**Response 200 (all healthy):**
```json
{
  "status": "ok",
  "pinecone": "ok",
  "neon": "ok",
  "s3": "ok"
}
```

**Response 503 (degraded):**
```json
{
  "status": "degraded",
  "pinecone": "ok",
  "neon": "unreachable",
  "s3": "ok"
}
```

---

### POST /ingest (JSON)

Ingests a text document for chunking, embedding, and vector storage.

**Request:**
```
Content-Type: application/json
```
```json
{
  "content": "Document text content...",
  "metadata": {"key": "value"}
}
```

**Response 202:**
```json
{
  "document_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "processing"
}
```

**Response 200 (duplicate):**
```json
{
  "document_id": "existing-doc-uuid",
  "status": "already_ingested"
}
```

---

### POST /ingest (File Upload)

Ingests a file (PDF, DOCX, DOC) via multipart upload. The file is extracted to text, uploaded to S3, and processed through the ingestion pipeline.

**Request:**
```
Content-Type: multipart/form-data
```
| Field | Type | Required | Description |
|---|---|---|---|
| `file` | file | yes | PDF, DOCX, or DOC file (max 20MB) |

**Response 202:**
```json
{
  "document_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "processing"
}
```

**Error Responses:**

| Status | Condition |
|---|---|
| 400 | Missing file field |
| 413 | File exceeds 20MB limit |
| 415 | Unsupported file type (not .pdf, .docx, .doc) |
| 422 | Text extraction failed (e.g., corrupt file) |
| 503 | Worker queue is full |

---

### GET /documents/{doc_id}/file

Returns a presigned S3 URL for downloading the original uploaded file.

**Response 200:**
```json
{
  "url": "https://bucket.s3.region.amazonaws.com/documents/...",
  "expires_in": 900
}
```

| Field | Description |
|---|---|
| `url` | Presigned GET URL for the file |
| `expires_in` | URL validity in seconds (900 = 15 minutes) |

**Error Responses:**

| Status | Condition |
|---|---|
| 400 | Invalid UUID format |
| 404 | No file stored for this document |

---

### POST /query

Queries the RAG pipeline with adaptive local/global retrieval.

**Request:**
```json
{
  "query": "What are Maria's skills?",
  "mode": "auto",
  "max_chunks": 10
}
```

**Response 200:**
```json
{
  "answer": "Maria has experience in...",
  "mode": "local",
  "chunks_used": 3,
  "token_usage": {
    "input_tokens": 450,
    "output_tokens": 120,
    "total_tokens": 570
  }
}
```
