.PHONY: test test-race lint run-query build tidy migrate-up migrate-down

# Run all tests
test:
	go test ./...

# Run tests with race detector (always use in CI)
test-race:
	go test -race ./...

# Lint (requires golangci-lint in PATH)
lint:
	golangci-lint run ./...

# Start the query service locally (requires .env to be sourced)
run-query:
	go run ./services/query/cmd/main.go

# Build the query service binary
build:
	go build -o bin/query ./services/query/cmd/main.go

# Apply pending DB migrations
migrate-up:
	migrate -path migrations -database "$$POSTGRES_DSN" up

# Roll back the last migration
migrate-down:
	migrate -path migrations -database "$$POSTGRES_DSN" down 1

# Tidy modules
tidy:
	go mod tidy

# Start local infrastructure (Postgres)
infra-up:
	docker-compose up -d postgres

# Stop local infrastructure
infra-down:
	docker-compose down
