# Contributing

## Prerequisites

- Go 1.27.3, matching go.mod.
- Docker with a running daemon for integration tests and image builds.
- golangci-lint v2.15.0, matching the CI configuration.

Read CODE_OF_CONDUCT.md before participating. Report security findings privately
as described in SECURITY.md, not in an issue or pull request.

## Local setup

Work from a local checkout and download dependencies:

```sh
go mod download
cp .env.example .env
```

The server reads process environment variables; it does not load .env itself.
Use a disposable local metadata database for development:

```sh
docker run --detach --name rag-dev-db \
  --env POSTGRES_DB=rag --env POSTGRES_USER=rag --env POSTGRES_PASSWORD=rag \
  --publish 127.0.0.1:5432:5432 postgres:16.13-alpine3.23
docker logs rag-dev-db
```

Wait until the logs report that the database accepts connections, then start the
server from the repository root:

```sh
export GO_RAG_FAKE_MODE=true
export DATABASE_URL='host=127.0.0.1 port=5432 user=rag password=rag dbname=rag sslmode=disable'
export SERVICE_TOKEN=local-development-only
go run ./cmd/server
```

Offline mode replaces the vector store, embedder and answer generator with local
implementations. It still needs the metadata database and applies embedded
migrations at startup. It does not use hosted credentials or file storage.
Never enable this mode in a deployed service. Keep development documents
synthetic and keep .env out of commits.

The server listens on port 8081 unless SERVER_PORT is set. Stop it before removing
the disposable database with `docker rm --force rag-dev-db`.

## Checks

Run these from the repository root before submitting a change:

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
golangci-lint run --timeout=5m --config=.golangci.yml
docker build --target runtime --tag quarry-rag:local .
```

Format changed source files with gofmt. The full test command includes integration
tests that start temporary database containers, so leave Docker running. Live
smoke tests are opt-in; do not enable RUN_SMOKE against shared or deployed data.

Place HTTP handlers and middleware under internal/api, pipeline stages under
internal/pipeline, and storage adapters under internal/store. cmd/server wires
these packages together. Keep operational commands separate under scripts.

## Database changes

Add a new numbered SQL file under internal/store/postgres/migrations using the
next unused three-digit number and a descriptive suffix. Files are embedded
automatically and applied once in version order, in a transaction under a
PostgreSQL advisory lock. The schema_migrations table records each version,
filename and checksum. Never edit an applied migration; add a new numbered file
instead. Test both a fresh database and an upgrade from the previous schema
with existing documents.

## Commits and pull requests

Use Conventional Commits subjects in this form:

```text
<type>(<scope>): <title>
```

Keep the entire subject under 72 characters. Use a lowercase package or component
scope and an imperative title, for example `fix(api): reject empty uploads`.
Choose feat, fix, refactor, perf, test, docs, build, ci or chore as the type.

Keep each pull request focused on one behavior. For a new API or schema change,
describe the use case in an issue first. Fill in the What, Why and How sections,
include the checks you ran, and explain compatibility or migration effects.
Add regression tests for fixes and update the relevant documentation and
CHANGELOG.md when caller-visible behavior changes. Wait for CI and maintainer
review, then address feedback before the change is merged.
