.PHONY: build run test clean docker-build docker-run dev deps fmt lint help

# Variables
BINARY_NAME=gateway
DOCKER_IMAGE=minisource/gateway
VERSION?=latest
GO_FILES=$(shell find . -name '*.go' -type f)

# Default target
all: deps fmt lint build

## help: Show this help message
help:
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Targets:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

## deps: Download dependencies
deps:
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy

## build: Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	CGO_ENABLED=0 go build -ldflags="-w -s" -o bin/$(BINARY_NAME) ./cmd

## build-linux: Build for Linux
build-linux:
	@echo "Building $(BINARY_NAME) for Linux..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-w -s" -o bin/$(BINARY_NAME)-linux ./cmd

## run: Run the gateway
run: build
	@echo "Running $(BINARY_NAME)..."
	./bin/$(BINARY_NAME)

## dev: Run in development mode with hot reload
dev:
	@echo "Running in development mode..."
	@if command -v air > /dev/null; then \
		air; \
	else \
		echo "Installing air..."; \
		go install github.com/cosmtrek/air@latest; \
		air; \
	fi

## test: Run tests
test:
	@echo "Running tests..."
	go test -v -race -cover ./...

## test-coverage: Run tests with coverage report
test-coverage:
	@echo "Running tests with coverage..."
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

## fmt: Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...
	@if command -v goimports > /dev/null; then \
		goimports -w .; \
	fi

## lint: Run linter
lint:
	@echo "Linting code..."
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping..."; \
	fi

## vet: Run go vet
vet:
	@echo "Running go vet..."
	go vet ./...

## clean: Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf bin/
	rm -f coverage.out coverage.html

## swagger: Generate Swagger documentation
swagger:
	@echo "Generating Swagger documentation..."
	@swag init -g cmd/main.go -o docs --parseDependency --parseInternal

## docker-build: Build Docker image
docker-build:
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE):$(VERSION) .

## docker-build-dev: Build Docker image for development
docker-build-dev:
	@echo "Building Docker image for development..."
	docker build --target builder -t $(DOCKER_IMAGE):dev .

## docker-run: Run Docker container
docker-run: docker-build
	@echo "Running Docker container..."
	docker run --rm -p 8080:8080 \
		-e AUTH_SERVICE_URL=http://host.docker.internal:5000 \
		-e NOTIFIER_SERVICE_URL=http://host.docker.internal:5001 \
		$(DOCKER_IMAGE):$(VERSION)

## docker-up: Start with docker-compose
docker-up:
	@echo "Starting services..."
	docker-compose up -d

## docker-up-dev: Start development stack
docker-up-dev:
	@echo "Starting development stack..."
	docker-compose -f docker-compose.dev.yml up -d

## docker-down: Stop docker-compose services
docker-down:
	@echo "Stopping services..."
	docker-compose down

## docker-down-dev: Stop development stack
docker-down-dev:
	@echo "Stopping development stack..."
	docker-compose -f docker-compose.dev.yml down

## docker-logs: View logs
docker-logs:
	docker-compose logs -f gateway

## docker-push: Push Docker image to registry
docker-push: docker-build
	@echo "Pushing Docker image..."
	docker push $(DOCKER_IMAGE):$(VERSION)

## health: Check gateway health
health:
	@curl -s http://localhost:8080/health | jq .

## ready: Check gateway readiness
ready:
	@curl -s http://localhost:8080/ready | jq .

## metrics: Get Prometheus metrics
metrics:
	@curl -s http://localhost:8080/metrics

## generate: Generate code (mocks, etc.)
generate:
	@echo "Generating code..."
	go generate ./...

## install-tools: Install development tools
install-tools:
	@echo "Installing development tools..."
	go install github.com/cosmtrek/air@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

## version: Show version
version:
	@echo "Version: $(VERSION)"
	@go version
