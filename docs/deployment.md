# Deployment

## Prerequisites

- Go 1.26+
- Docker 27+ (for containerized deployment)
- AWS account with Bedrock and S3 access
- Pinecone index (dimension 1024, cosine metric)
- PostgreSQL database (Neon recommended)

## Local Development

1. Copy `.env.example` to `.env` and fill in credentials.
2. Build and run:
   ```bash
   go build -o server ./cmd/server
   ./server
   ```
3. The server starts on the port specified by `SERVER_PORT` (default 8081).

## Docker

Build and run with Docker Compose:
```bash
docker compose up --build
```

The Dockerfile provides four stages:
- **test** — Runs `go vet` and `go test -race`.
- **lint** — Runs `golangci-lint` with the project configuration.
- **builder** — Compiles the binary with `CGO_ENABLED=0`.
- **runtime** — Minimal Alpine image with the compiled binary.

## Database Migrations

Migrations are embedded in the Go binary and applied automatically at startup. No external migration tool is needed. The three migration files are:

1. `001_init.sql` — Core schema (documents, chunks, fingerprints).
2. `002_add_file_storage.sql` — File storage columns (file_name, file_type, s3_key).
3. `003_add_content_preview.sql` — Named entity extraction column (content_preview).

## S3 Bucket Setup

Create an S3 bucket matching the `S3_BUCKET` environment variable. The IAM user needs:

### Required Permissions
- `s3:PutObject` — For file uploads to `documents/*/*/*`.
- `s3:GetObject` — For presigned URL generation.
- `s3:HeadBucket` — For health checks.
- `s3:DeleteObject` — For cleanup operations (optional).

### IAM Policy Example
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:PutObject",
        "s3:GetObject",
        "s3:DeleteObject"
      ],
      "Resource": "arn:aws:s3:::your-bucket-name/documents/*"
    },
    {
      "Effect": "Allow",
      "Action": ["s3:HeadBucket"],
      "Resource": "arn:aws:s3:::your-bucket-name"
    }
  ]
}
```

### Bucket Configuration
- No public access required.
- Versioning optional (recommended for production).
- Default encryption enabled (SSE-S3).
- Bucket policy and CORS are not required for server-side operations. If clients download files directly via presigned URLs, ensure the bucket allows GET requests from the expected origins.

## Health Monitoring

`GET /health` returns the status of all external dependencies:
```json
{"status":"ok","pinecone":"ok","neon":"ok","s3":"ok"}
```

Each probe has a 5-second timeout. A degraded status returns HTTP 503.
