.PHONY: build test lint vet ci up down logs cleanup validate

# Run inside container — no host deps needed
build:
	docker build --target builder -t go-ms-rag:build .

# Run static analysis on the host.
vet:
	go vet ./...

lint:
	golangci-lint run ./...

# Full tests need a running Docker daemon for isolated database containers.
test:
	go test -race -count=1 ./...

# Run host quality gates before building the runtime image.
ci:
	$(MAKE) vet
	$(MAKE) lint
	$(MAKE) test
	docker build --target runtime -t go-ms-rag:prod .

# Start the full stack
up:
	docker-compose up --build -d

down:
	docker-compose down

logs:
	docker-compose logs -f app

# Delete explicit document UUIDs: make cleanup IDS="<uuid> <uuid>".
cleanup:
	go run ./scripts/cleanup $(IDS)

validate:
	go run ./scripts/validate/main.go
