FROM golang:1.27.3-alpine3.23 AS base
RUN apk add --no-cache gcc musl-dev git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# ── test stage ──────────────────────────────────────────
FROM base AS test
COPY . .
RUN go vet ./...
RUN CGO_ENABLED=1 go test -short ./... -count=1 -race

# ── lint stage ──────────────────────────────────────────
FROM base AS lint
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
COPY . .
RUN golangci-lint run --config=.golangci.yml

# ── builder stage ───────────────────────────────────────
FROM base AS builder
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/server ./cmd/server

# ── final runtime image (minimal) ───────────────────────
FROM alpine:3.23.4 AS runtime
RUN apk add --no-cache ca-certificates tzdata && addgroup -S -g 10001 app && adduser -S -u 10001 -G app app
WORKDIR /app
COPY --from=builder /bin/server /app/server
COPY internal/store/postgres/migrations ./internal/store/postgres/migrations
USER 10001:10001
EXPOSE 8081
ENTRYPOINT ["/app/server"]
