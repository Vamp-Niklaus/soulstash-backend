.PHONY: all build run test test-integration test-race bench lint cover load docker clean help

APP_NAME ?= soulstash-server
MAIN_PATH ?= ./cmd/server
PORT ?= 3000

all: lint test-race build

## build: Compile binary
build:
	go build -v -ldflags="-w -s" -o bin/$(APP_NAME) $(MAIN_PATH)

## run: Run server locally
run:
	go run $(MAIN_PATH)

## test: Run unit tests with parallel execution
test:
	go test -v -parallel=4 ./...

## test-race: Run tests with data race detector
test-race:
	go test -race -v ./...

## test-integration: Run integration tests (requires local mongodb)
test-integration:
	go test -tags=integration -race -v ./...

## bench: Run performance benchmarks with memory allocations
bench:
	go test -bench=. -benchmem -run=^$$ ./...

## lint: Run golangci-lint
lint:
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run ./... || go vet ./...

## cover: Run tests with coverage profile and output report
cover:
	go test -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out

## load: Run load tests
load:
	@echo "Running load tests with k6 or vegeta..."
	@which k6 > /dev/null 2>&1 && k6 run loadtest/k6_scenario.js || echo "k6 not found; install k6 to run load testing"

## docker: Build non-root production Docker image
docker:
	docker build -t $(APP_NAME):latest .

## clean: Clean build artifacts and test coverage
clean:
	rm -rf bin/ coverage.out
