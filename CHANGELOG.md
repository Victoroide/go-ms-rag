# Changelog

Notable changes are grouped by release.

## [Unreleased]

### Added

- JSON and multipart document ingestion with content deduplication, asynchronous
  processing and caller-supplied document identifiers.
- Text extraction, normalization, semantic chunking and chunk quality metadata.
- Local and global retrieval with document type filters, scored evidence and
  context-based answer generation.
- Document reference listing, deletion and temporary original-file downloads.
- Local offline mode, health probes and environment-based configuration.
- Automated checks, dependency updates, security analysis and version-tagged
  multi-architecture runtime images.

### Changed

- Document labels default to `document` when omitted and are never inferred from
  content; migration 005 changes the database default without relabelling rows.
- Evidence-backed answers follow the question's language. Empty authorized
  results return a fixed English no-information answer without synthesis and
  with zero synthesis token usage.
- Blank content or queries, unknown retrieval modes, `max_chunks` outside 0–100,
  and trailing JSON values return 400.
- JSON bodies over 8,097,152 bytes for ingestion or 1 MiB for queries return 413.
  Ingestion retains its 1,000,000-byte decoded-content limit; the body allowance
  includes worst-case JSON escaping and metadata overhead.
- Extraction-limit failures return 422, including PDF text above 1,000,000 bytes,
  PDF work-budget exhaustion and DOCX document XML above 16 MiB.
- Document lookup and external deletion failures return 500 rather than a false
  missing-document or success result; already-missing vector namespaces are
  accepted on retries.
- Response snippets and document previews truncate at UTF-8 character boundaries.
- Startup rejects malformed numbers and booleans, unsupported synthesis policies
  and embedding dimensions, and values outside documented configuration bounds.
- Automatic retrieval falls back to GLOBAL if its similarity probe fails.
- The runtime uses numeric UID/GID 10001; local validation targets run on the host,
  and the Docker test stage runs the short suite without a Docker daemon.
- The deletion command requires explicit document UUIDs, accepts them through
  `make cleanup IDS="<uuid> ..."`, and reuses connections across documents.

### Fixed

- Wait for active requests and accepted ingestion work before closing stores;
  repeated shutdown and concurrent enqueue no longer race channel closure.
- Persist failure status with a bounded independent context after cancellation.
- Roll back failed chunk batches, refresh conflicting chunks, propagate metadata
  errors and isolate entity caches by database connection pool.
- Preserve DOCX text across formatting runs, bound PDF stream expansion, and
  retain chunk quality metadata without sharing mutable caller maps.
- Remove multipart temporary files and source uploads rejected by a full queue.
- Defer live-test deletion until asynchronous ingestion has stored the document.
- Report panic responses as JSON and log the status actually written.

### Security

- Service token authentication for document and query routes, with startup checks
  that reject missing tokens outside offline mode.
- Document allowlists applied before answer generation for restricted queries.
- Upgrade affected dependencies to remove reachable vulnerability findings and
  enforce header, idle and request timeout boundaries.
