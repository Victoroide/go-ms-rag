.PHONY: build test lint vet ci up down logs

# Run inside container — no host deps needed
build:
	docker build --target builder -t go-ms-rag:build .

vet:
	docker build --target test -t go-ms-rag:test . \
		--build-arg RUN_VET=1

lint:
	docker build --target lint -t go-ms-rag:lint .

test:
	docker build --target test -t go-ms-rag:test .

# Run all quality gates in order (CI equivalent)
ci:
	docker build --target test  --no-cache -t go-ms-rag:test .
	docker build --target lint  --no-cache -t go-ms-rag:lint .
	docker build --target builder             -t go-ms-rag:prod .

# Start the full stack
up:
	docker-compose up --build -d

down:
	docker-compose down

logs:
	docker-compose logs -f app
