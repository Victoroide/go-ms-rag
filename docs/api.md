# API Reference

All API responses use `Content-Type: application/json`. Error responses follow a uniform structured JSON format:

```json
{
  "error": "<error_category>",
  "message": "<descriptive_message>"
}
```

## Authentication

When `SERVICE_TOKEN` is configured, all mutating and sensitive endpoints require the `X-Service-Token` HTTP header matching the configured token value:
- `POST /ingest`
- `POST /query`
- `GET /documents`
- `GET /documents/{doc_id}/file`
- `DELETE /documents/{doc_id}`

Requests missing or presenting an incorrect token receive `401 Unauthorized`. Header comparisons are performed in constant time using `crypto/subtle.ConstantTimeCompare`. The `GET /health` endpoint is public and does not require authentication.

---

## Endpoints

### GET /health

Returns service health status and ping probes for backing data stores.

- **Method**: `GET`
- **Path**: `/health`
- **Auth**: None (public)
- **Request Body**: None

#### Responses

- **200 OK**: All dependency probes responded within the 5-second timeout.
- **503 Service Unavailable**: One or more backing stores failed to respond within the timeout.

Response payload fields:

| Field | Type | Description |
|---|---|---|
| `status` | string | `"ok"` if all dependencies respond, or `"degraded"` if any probe fails |
| `pinecone` | string | `"ok"` or `"unreachable"` |
| `neon` | string | `"ok"` or `"unreachable"` |
| `s3` | string | `"ok"` or `"unreachable"` |

#### Example

```sh
curl localhost:8081/health
```

---

### POST /ingest

Ingests textual content or document files for chunking, embedding generation, vector indexing, and relational storage. Supports JSON payloads and multipart file uploads.

- **Method**: `POST`
- **Path**: `/ingest`
- **Auth**: Required (`X-Service-Token`)

#### Variant A: JSON Request

- **Content-Type**: `application/json` (accepts standard parameters such as `charset=utf-8`)
- **Body Limit**: 8,097,152 bytes (six times the decoded-content limit plus 2 MiB of overhead)

Request fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `content` | string | yes | Document text content (maximum 1,000,000 bytes after JSON decoding) |
| `doc_type` | string | no | Document category label. Defaults to `"document"` if omitted |
| `document_id` | string | no | Optional document UUID, retained in canonical form. Omitted or empty values generate a fresh UUID; malformed non-empty values return 400 |
| `metadata` | object | no | Optional arbitrary key-value metadata object stored in PostgreSQL and vector metadata |

Response (New Document):
- **Status**: `202 Accepted`

```json
{
  "document_id": "8a3e792c-5df6-4b6e-a612-4f7db4959db2",
  "status": "processing"
}
```

Response (Duplicate Document):
- **Status**: `200 OK`

When `document_id` is supplied, an existing identifier with the same content
fingerprint returns `already_ingested` with that same identifier. Different
content under an existing identifier returns 409 with
`{"error":"conflict","message":"document_id already exists with different content"}`.
Existing documents are never overwritten.

Content deduplication across identifiers applies only when `document_id` is
omitted or empty. A new caller-supplied identifier is retained even if another
document has identical content. Document or fingerprint lookup failures return
503 with `{"error":"unavailable","message":"document store unavailable"}`.

```json
{
  "document_id": "8a3e792c-5df6-4b6e-a612-4f7db4959db2",
  "status": "already_ingested"
}
```

Error Status Codes:

| Status | Error Category | Condition |
|---|---|---|
| `400 Bad Request` | `validation` | Content field missing, empty, blank whitespace, or exceeding 1,000,000 decoded bytes; trailing characters after JSON body; malformed JSON; non-empty `document_id` is not a UUID (`document_id must be a UUID`) |
| `401 Unauthorized` | `unauthorized` | Missing or invalid `X-Service-Token` header |
| `409 Conflict` | `conflict` | Supplied `document_id` already exists with different content |
| `413 Payload Too Large` | `validation` | JSON request payload exceeds 8,097,152 bytes |
| `415 Unsupported Media Type` | `validation` | Content-Type is not `application/json` or `multipart/form-data` |
| `503 Service Unavailable` | `queue` | Ingest worker channel queue is full (`WORKER_QUEUE_SIZE` reached) |
| `503 Service Unavailable` | `unavailable` | Document or fingerprint lookup failed |

Example:

```sh
curl -X POST localhost:8081/ingest \
  -H "X-Service-Token: dev-token" \
  -H "Content-Type: application/json" \
  -d '{
    "content": "Operational procedures for network routing nodes...",
    "doc_type": "handbook",
    "metadata": {
      "author": "infrastructure-team",
      "version": "1.4"
    }
  }'
```

#### Variant B: Multipart Form Upload

- **Content-Type**: `multipart/form-data`
- **Body Limit**: Bounded by `MAX_UPLOAD_BYTES` (default 20,971,520 bytes = 20 MiB)

Form fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `file` | file binary | yes | Binary file to extract and ingest. Accepted formats: PDF, DOCX, DOC |
| `doc_type` | string | no | Document category label. Defaults to `"document"` if omitted |
| `document_id` | string | no | Optional document UUID with the same identity and conflict rules as JSON requests, applied to extracted text |

New uploads return 202 with the retained or generated identifier. Identical
resubmissions under an existing identifier return 200 `already_ingested` with
the same identifier. Without `document_id`, uploads use content deduplication.

Error Status Codes:

| Status | Error Category | Condition |
|---|---|---|
| `400 Bad Request` | `validation` | Form `file` part missing or read error; non-empty `document_id` is not a UUID |
| `401 Unauthorized` | `unauthorized` | Missing or invalid `X-Service-Token` header |
| `409 Conflict` | `conflict` | Supplied `document_id` already exists with different extracted text |
| `413 Payload Too Large` | `validation` | Upload exceeds `MAX_UPLOAD_BYTES` |
| `415 Unsupported Media Type` | `validation` | Uploaded file extension is not pdf, docx, or doc |
| `422 Unprocessable Entity` | `extraction` | Document text extraction failed (e.g., malformed binary structure, extracted PDF, DOCX, DOC or text exceeding 1,000,000 bytes, expanded DOCX XML exceeding 16 MiB, a DOC stream declaring a size larger than the file, or PDF extraction work exceeding its limits) |
| `422 Unprocessable Entity` | `extraction` | Isolated parser failure: `document could not be parsed`; extraction timeout: `document extraction exceeded the time limit` |
| `503 Service Unavailable` | `busy` | All upload slots remain occupied beyond `EXTRACT_QUEUE_TIMEOUT`: `too many concurrent uploads`, with `Retry-After: 5` |
| `503 Service Unavailable` | `extraction` | The extraction child cannot be started: `document extraction is unavailable` |
| `503 Service Unavailable` | `queue` | Ingest worker channel queue is full; rolls back S3 object if uploaded |
| `503 Service Unavailable` | `unavailable` | Document or fingerprint lookup failed |

Example:

```sh
curl -X POST localhost:8081/ingest \
  -H "X-Service-Token: dev-token" \
  -F "file=@technical_paper.pdf" \
  -F "doc_type=research"
```

---

### POST /query

Executes vector similarity search and synthesizes an answer grounded in the retrieved document corpus.

- **Method**: `POST`
- **Path**: `/query`
- **Auth**: Required (`X-Service-Token`)
- **Content-Type**: `application/json`
- **Body Limit**: 1 MiB (1,048,576 bytes)

Request fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `query` | string | yes | Natural-language query string (1 to 10,000 decoded bytes) |
| `mode` | string | no | Retrieval mode: `"auto"` (default), `"local"`, or `"global"`. Case-insensitive |
| `filter_doc_type` | string | no | Restricts vector retrieval to chunks matching the given `doc_type` |
| `max_chunks` | int | no | Maximum chunks to retrieve (0 to 100). Defaults to `MAX_CHUNKS_PER_QUERY` (10) |
| `allowed_document_ids` | array of strings | no | Tenant/role allowlist of UUIDs, at most `MAX_ALLOWED_DOCUMENT_IDS` entries (default 25000). IDs are canonicalized and deduplicated. Omitted or `null` means unrestricted unless `REQUIRE_DOCUMENT_ALLOWLIST=true`, which makes a non-null field mandatory. An empty list (`[]`) returns the no-information answer without embedding or search. The allowlist is applied inside vector search and routing |

Response fields (200 OK):

| Field | Type | Description |
|---|---|---|
| `answer` | string | Grounded answer in the question's language when evidence exists; fixed English no-information answer otherwise |
| `mode` | string | Retrieval mode executed: `"LOCAL"` or `"GLOBAL"`; automatic routing reflects only authorized, visible candidate documents |
| `chunks_used` | int | Count of chunks from completed documents remaining after visibility and allowlist filtering |
| `context_chunks` | int | Count of chunks retained after score thresholding and passed into the LLM context (optional) |
| `context_tokens` | int | Estimated token count of the prompt context (optional) |
| `early_exit` | bool | Indicates whether query execution completed early (optional) |
| `stage_timings_ms` | map[string]int64 | Execution timings for pipeline stages in milliseconds (optional) |
| `token_usage` | object | LLM token usage metrics (`input_tokens`, `output_tokens`, `total_tokens`) |
| `scored_chunks` | array | Retrieved chunks ordered by similarity score |

Malformed allowlist entries return 400 `allowed_document_ids must contain UUIDs`; a missing or null required allowlist returns 400 `allowed_document_ids is required`. Lists above `MAX_ALLOWED_DOCUMENT_IDS` return 400 before deduplication.

If retrieval or the document allowlist leaves no evidence, the answer is `No relevant information was found to answer this question.` No synthesis model is called, `token_usage` fields are zero, and `early_exit` is true. A transient similarity-probe failure selects GLOBAL retrieval in automatic mode instead of failing the query; embedding or subsequent retrieval failures still return 500.

Document visibility is checked against PostgreSQL before answer generation. Older processing rows with a stored fingerprint are treated as completed for compatibility. Failed, unfinished and missing documents contribute no chunks; a visibility lookup failure returns 500. The same empty-evidence response applies when visibility filtering removes all chunks.

Scored chunk fields:

| Field | Type | Description |
|---|---|---|
| `doc_id` | string | Document UUID the chunk belongs to |
| `doc_type` | string | Document category label |
| `chunk_index` | int | Position index of the chunk within the source document |
| `score` | float64 | Cosine similarity score |
| `text` | string | Chunk text excerpt, truncated safely to a maximum of 600 runes without splitting UTF-8 characters |
| `kind` | string | Classification category if populated during ingestion (optional) |
| `quality` | string | Quality rating if populated during ingestion (optional) |

Error Status Codes:

| Status | Error Category | Condition |
|---|---|---|
| `400 Bad Request` | `validation` | Query missing, empty, or whitespace; query exceeding 10,000 decoded bytes; mode not auto, local, or global; max_chunks outside 0-100; trailing JSON data; malformed JSON |
| `401 Unauthorized` | `unauthorized` | Missing or invalid `X-Service-Token` header |
| `413 Payload Too Large` | `validation` | Request body exceeds the 1 MiB limit |
| `415 Unsupported Media Type` | `validation` | Content-Type is not `application/json` |
| `500 Internal Server Error` | `pipeline` | Unhandled error in query decomposition, embedding, vector retrieval, or model synthesis |

Example:

```sh
curl -X POST localhost:8081/query \
  -H "X-Service-Token: dev-token" \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Which routing algorithms are supported in version 2?",
    "mode": "auto",
    "filter_doc_type": "handbook",
    "max_chunks": 5
  }'
```

---

### GET /documents

Returns metadata references for all stored documents. Used by calling services for inventory verification and orphan vector reconciliation.

- **Method**: `GET`
- **Path**: `/documents`
- **Auth**: Required (`X-Service-Token`)
- **Request Body**: None

Response fields (200 OK):

| Field | Type | Description |
|---|---|---|
| `documents` | array | List of stored document references |
| `count` | int | Total number of documents returned |

Document reference fields:

| Field | Type | Description |
|---|---|---|
| `document_id` | string | Document UUID |
| `doc_type` | string | Document category label |
| `s3_key` | string | S3 object key, or omitted if no binary file was stored |

Error Status Codes:

| Status | Error Category | Condition |
|---|---|---|
| `401 Unauthorized` | `unauthorized` | Missing or invalid `X-Service-Token` header |
| `500 Internal Server Error` | `internal` | Database query failure |

Example:

```sh
curl localhost:8081/documents \
  -H "X-Service-Token: dev-token"
```

---

### GET /documents/{doc_id}/file

Generates a presigned S3 URL for downloading the original binary file associated with an ingested document. Downloads are served as attachments using the stored filename and a content type derived from its extension.

- **Method**: `GET`
- **Path**: `/documents/{doc_id}/file`
- **Auth**: Required (`X-Service-Token`)
- **Parameters**: `doc_id` (path parameter, must be a valid UUID)
- **Request Body**: None

Response fields (200 OK):

| Field | Type | Description |
|---|---|---|
| `url` | string | Presigned GET URL generated for the S3 object |
| `expires_in` | int | URL validity duration in seconds (always 900 seconds = 15 minutes) |

Error Status Codes:

| Status | Error Category | Condition |
|---|---|---|
| `400 Bad Request` | `validation` | Invalid path or `doc_id` is not a valid UUID |
| `401 Unauthorized` | `unauthorized` | Missing or invalid `X-Service-Token` header |
| `404 Not Found` | `not_found` | S3 storage not configured or no file stored for the requested document |
| `500 Internal Server Error` | `internal` | Database lookup failure or error generating presigned URL |

Example:

```sh
curl localhost:8081/documents/8a3e792c-5df6-4b6e-a612-4f7db4959db2/file \
  -H "X-Service-Token: dev-token"
```

---

### DELETE /documents/{doc_id}

Deletes a document and all related artifacts across Pinecone, S3, and PostgreSQL.

Deletion continues after the caller disconnects, with a separate 60-second deadline. When a stored file exists but file storage is not configured, deletion returns `503 Service Unavailable` before removing any vectors or metadata, with `{"error":"storage","message":"stored file cannot be deleted because file storage is not configured"}`.

- **Method**: `DELETE`
- **Path**: `/documents/{doc_id}`
- **Auth**: Required (`X-Service-Token`)
- **Parameters**: `doc_id` (path parameter, must be a valid UUID)
- **Request Body**: None

Deletion ordering:
1. Removes the legacy Pinecone document namespace (`doc_id`) when present.
2. Deletes vector entries in the Pinecone default namespace where metadata `doc_id` matches.
3. Deletes the original uploaded file from S3 if an `s3_key` exists.
4. Deletes the document record from PostgreSQL (cascading foreign keys remove rows in `chunks` and `document_fingerprints`).

If an error occurs while deleting vectors or object storage, PostgreSQL metadata is preserved so the operation can be retried, and the request returns `500 Internal Server Error`.

After deletion returns successfully, subsequent queries exclude the document from answers and scored chunks immediately, even while vector deletion is still propagating through the index.

Response fields (200 OK):

| Field | Type | Description |
|---|---|---|
| `deleted` | bool | Always `true` on successful deletion |
| `doc_id` | string | UUID of the deleted document |

Error Status Codes:

| Status | Error Category | Condition |
|---|---|---|
| `400 Bad Request` | `validation` | Invalid path or `doc_id` is not a valid UUID |
| `401 Unauthorized` | `unauthorized` | Missing or invalid `X-Service-Token` header |
| `404 Not Found` | `not_found` | Document does not exist in PostgreSQL |
| `500 Internal Server Error` | `internal` | Database query failure or external deletion error in Pinecone or S3 |
| `503 Service Unavailable` | `storage` | A stored file exists but file storage is not configured; no data is deleted |

Example:

```sh
curl -X DELETE localhost:8081/documents/8a3e792c-5df6-4b6e-a612-4f7db4959db2 \
  -H "X-Service-Token: dev-token"
```
