FROM golang:1.26-alpine AS base
RUN apk add --no-cache gcc musl-dev git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# ── test stage ──────────────────────────────────────────
FROM base AS test
COPY . .
RUN go vet ./...
RUN CGO_ENABLED=1 go test ./... -count=1 -race

# ── lint stage ──────────────────────────────────────────
FROM base AS lint
RUN go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
COPY . .
RUN golangci-lint run --config=.golangci.yml

# ── builder stage ───────────────────────────────────────
FROM base AS builder
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/server ./cmd/server

# ── final runtime image (minimal) ───────────────────────
FROM alpine:3.21 AS runtime
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /bin/server /app/server
COPY internal/store/postgres/migrations ./internal/store/postgres/migrations
EXPOSE 8081
ENTRYPOINT ["/app/server"]
