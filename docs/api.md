# API Reference

All endpoints return `Content-Type: application/json`. Error responses use the `APIError` struct:

```json
{"error": "<error_type>", "message": "<human_readable_message>"}
```

---

### GET /health

**Description:** Returns service health status including connectivity to Pinecone, PostgreSQL (Neon), and S3.

**Request**

No body. No parameters.

**Response 200 (all healthy)**

| Field | Type | Description |
|---|---|---|
| `status` | `string` | `"ok"` when all probes pass |
| `pinecone` | `string` | `"ok"` or `"unreachable"` |
| `neon` | `string` | `"ok"` or `"unreachable"` |
| `s3` | `string` | `"ok"` or `"unreachable"` |

**Response 503 (degraded)**

Same fields, but `status` is `"degraded"` and at least one probe is `"unreachable"`.

**Error Responses**

| HTTP | Condition |
|---|---|
| 503 | One or more dependencies unreachable (5s timeout per probe) |

**Side Effects**

None. Read-only health probes.

**Example**

```bash
curl http://localhost:8081/health
```

---

### POST /ingest

**Description:** Ingests a document for chunking, embedding, and vector storage. Supports two content types: JSON body or multipart file upload.

#### JSON Variant

**Request**

Content-Type: `application/json`

| Field | Type | Required | Description |
|---|---|---|---|
| `content` | `string` | yes | Document text (max 1,000,000 characters) |
| `metadata` | `object` | no | Arbitrary key-value metadata |
| `doc_type` | `string` | no | Explicit document type override (`"candidate"`, `"job"`, etc.). If omitted, auto-detected by PersistStage. |

**Response 202 (new document)**

| Field | Type | Description |
|---|---|---|
| `document_id` | `string` | UUID of the new document |
| `status` | `string` | `"processing"` |

**Response 200 (duplicate)**

| Field | Type | Description |
|---|---|---|
| `document_id` | `string` | UUID of the existing document |
| `status` | `string` | `"already_ingested"` |

**Error Responses**

| HTTP | `error` | `message` |
|---|---|---|
| 400 | `validation` | `"invalid JSON body"` |
| 400 | `validation` | `"content is required"` |
| 400 | `validation` | `"content exceeds maximum length of 1000000 characters"` |
| 415 | `validation` | `"Content-Type must be application/json or multipart/form-data"` |
| 503 | `queue` | `"ingest queue is full"` |

**Side Effects**

- Computes SHA-256 fingerprint of content and checks for duplicates in Postgres.
- On new document: enqueues `IngestTask` to worker. Worker runs ChunkStage -> EmbedStage -> PersistStage, which upserts vectors to Pinecone (document namespace + default namespace), inserts document/chunk records to Postgres, stores fingerprint.

**Example**

```bash
curl -X POST http://localhost:8081/ingest \
  -H "Content-Type: application/json" \
  -d '{"content":"Sofia Mamani - Backend Engineer...","doc_type":"candidate"}'
```

#### Multipart Variant

**Request**

Content-Type: `multipart/form-data`

| Field | Type | Required | Description |
|---|---|---|---|
| `file` | file | yes | PDF, DOCX, or DOC file (max 20MB, controlled by `MAX_UPLOAD_BYTES`) |
| `doc_type` | `string` | no | Explicit document type override. If omitted, auto-detected from extracted text. |

**Response 202 (new document)**

Same as JSON variant.

**Response 200 (duplicate)**

Same as JSON variant.

**Error Responses**

| HTTP | `error` | `message` |
|---|---|---|
| 400 | `validation` | `"file field is required"` |
| 413 | `validation` | `"file exceeds maximum size of 20MB"` |
| 415 | `validation` | `"unsupported file type, accepted: pdf, docx, doc"` |
| 422 | `extraction` | (extraction error message) |
| 503 | `queue` | `"ingest queue is full"` |

**Side Effects**

- Extracts text from the uploaded file.
- Computes fingerprint of extracted text and checks for duplicates.
- Uploads the original file to S3 at `documents/{doc_type}/{doc_id}/{filename}` (non-fatal if S3 fails).
- Enqueues `IngestTask` with `FileName`, `FileType`, `S3Key`, and `DocType` fields set.

**Example**

```bash
curl -X POST http://localhost:8081/ingest \
  -F "file=@resume.pdf" \
  -F "doc_type=candidate"
```

---

### POST /query

**Description:** Queries the RAG pipeline with adaptive local/global retrieval and LLM-based answer synthesis.

**Request**

Content-Type: `application/json`

| Field | Type | Required | Description |
|---|---|---|---|
| `query` | `string` | yes | Query text (max 10,000 characters) |
| `mode` | `string` | no | Retrieval mode: `"auto"` (default), `"local"`, or `"global"`. Case-insensitive. |
| `filter_doc_type` | `string` | no | If set, only chunks with matching `doc_type` metadata are returned from Pinecone. |
| `max_chunks` | `int` | no | Maximum chunks to retrieve. Defaults to `MAX_CHUNKS_PER_QUERY` (10). |

**Response 200**

| Field | Type | Description |
|---|---|---|
| `answer` | `string` | Synthesized answer from LLM |
| `mode` | `string` | Retrieval mode used: `"LOCAL"` or `"GLOBAL"` |
| `chunks_used` | `int` | Number of chunks used in synthesis |
| `token_usage` | `object` | LLM token consumption |
| `token_usage.input_tokens` | `int32` | Tokens sent to LLM |
| `token_usage.output_tokens` | `int32` | Tokens generated by LLM |
| `token_usage.total_tokens` | `int32` | Sum of input + output |
| `scored_chunks` | `array` | Per-chunk scoring details |

Each element in `scored_chunks`:

| Field | Type | Description |
|---|---|---|
| `doc_id` | `string` | Document UUID the chunk belongs to |
| `doc_type` | `string` | Document type from chunk metadata (e.g. `"candidate"`, `"job"`) |
| `chunk_index` | `int` | Zero-based chunk position within the document |
| `score` | `float64` | Cosine similarity score from Pinecone |
| `text` | `string` | Chunk text, truncated to 200 characters |

**Error Responses**

| HTTP | `error` | `message` |
|---|---|---|
| 400 | `validation` | `"invalid JSON body"` |
| 400 | `validation` | `"query is required"` |
| 400 | `validation` | `"query exceeds maximum length of 10000 characters"` |
| 415 | `validation` | `"Content-Type must be application/json"` |
| 500 | `pipeline` | (pipeline error message) |

**Side Effects**

None. Read-only. Calls Bedrock for query decomposition and synthesis, queries Pinecone for vector search.

**Example**

```bash
curl -X POST http://localhost:8081/query \
  -H "Content-Type: application/json" \
  -d '{"query":"Which candidates have Python experience?","filter_doc_type":"candidate","mode":"auto"}'
```

---

### GET /documents/{doc_id}/file

**Description:** Returns a presigned S3 URL for downloading the original uploaded file.

**Request**

No body. `{doc_id}` must be a valid UUID in the URL path.

**Response 200**

| Field | Type | Description |
|---|---|---|
| `url` | `string` | Presigned S3 GET URL |
| `expires_in` | `int` | URL validity in seconds (always `900` = 15 minutes) |

**Error Responses**

| HTTP | `error` | `message` |
|---|---|---|
| 400 | `validation` | `"invalid path"` |
| 400 | `validation` | `"doc_id must be a valid UUID"` |
| 404 | `not_found` | `"no file stored for this document"` |
| 500 | `internal` | `"failed to look up document"` |
| 500 | `internal` | `"failed to generate download URL"` |

**Side Effects**

None. Reads S3 key from Postgres, generates presigned URL.

**Example**

```bash
curl http://localhost:8081/documents/550e8400-e29b-41d4-a716-446655440000/file
```

---

### DELETE /documents/{doc_id}

**Description:** Deletes a document and all associated data from Pinecone, S3, and Postgres.

**Request**

No body. `{doc_id}` must be a valid UUID in the URL path.

**Response 200**

| Field | Type | Description |
|---|---|---|
| `deleted` | `bool` | Always `true` on success |
| `doc_id` | `string` | The deleted document's UUID |

**Error Responses**

| HTTP | `error` | `message` |
|---|---|---|
| 400 | `validation` | `"invalid path"` |
| 400 | `validation` | `"doc_id must be a valid UUID"` |
| 404 | `not_found` | `"document not found"` |
| 500 | `internal` | `"failed to delete document"` |

**Side Effects**

Executes in order:
1. Queries Postgres for the document (verifies existence, retrieves `s3_key`).
2. Deletes all vectors in the Pinecone namespace matching `doc_id` (via `DeleteAllVectorsInNamespace`).
3. Deletes vectors from the default Pinecone namespace where metadata `doc_id` matches (via `DeleteVectorsByFilter`).
4. Deletes the file from S3 if `s3_key` is not null.
5. Deletes the document from Postgres (cascades to `chunks` and `document_fingerprints` via FK ON DELETE CASCADE).

Pinecone and S3 errors are logged but do not fail the request. Only a Postgres delete failure returns HTTP 500.

**Example**

```bash
curl -X DELETE http://localhost:8081/documents/550e8400-e29b-41d4-a716-446655440000
```
