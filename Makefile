.PHONY: build install clean test lint fmt vet proto docker-build docker-up docker-down help

# Build both binaries
build:
	@echo "Building mimorid and mimorictl..."
	@mkdir -p bin
	go build -o bin/mimorid ./cmd/mimorid
	go build -o bin/mimorictl ./cmd/mimorictl
	@echo "Build complete! Binaries are in ./bin/"

# Install binaries to $GOPATH/bin or $GOBIN (if set)
install:
	@echo "Installing mimorid and mimorictl..."
	go install ./cmd/mimorid
	go install ./cmd/mimorictl
	@echo "Install complete! Add \$$(go env GOPATH)/bin to your PATH"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf bin/
	go clean ./...

# Run all tests
test:
	go test ./...

# Run e2e tests
test-e2e:
	go test ./tests -v

# Run all tests (unit + e2e)
test-all:
	go test ./... -v

# Run chaos tests
test-chaos:
	go test ./tests -v -run TestChaos -timeout 5m

# Run load tests
test-load:
	go test ./tests -v -run TestLoad -timeout 5m

# Run network partition tests
test-partition:
	go test ./tests -v -run TestNetworkPartition -timeout 5m

# Run stress tests
test-stress:
	go test ./tests -v -run TestStress -timeout 5m

# Run specific test
test-follower-reads:
	go test ./tests -v -run TestFollowerReads

# Code quality
fmt:
	@echo "Formatting code..."
	go fmt ./...
	gofmt -s -w .

vet:
	@echo "Running go vet..."
	go vet ./...

lint: fmt vet
	@echo "Linting complete!"

# Docker targets
docker-build:
	@echo "Building Docker image..."
	docker-compose build

docker-up:
	@echo "Starting cluster..."
	docker-compose up -d
	@echo "Cluster started! Wait a few seconds for leader election."

docker-down:
	@echo "Stopping cluster..."
	docker-compose down

docker-logs:
	docker-compose logs -f

# Proto generation (if needed)
proto:
	@echo "Regenerating proto files..."
	@echo "Note: protoc must be installed"
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/*.proto

# Help
help:
	@echo "Mimori Build System"
	@echo ""
	@echo "Build:"
	@echo "  make build          - Build binaries to ./bin/"
	@echo "  make install        - Install binaries to GOPATH/bin"
	@echo "  make clean          - Remove build artifacts"
	@echo ""
	@echo "Testing:"
	@echo "  make test           - Run all tests"
	@echo "  make test-e2e       - Run e2e tests"
	@echo "  make test-chaos     - Run chaos tests"
	@echo "  make test-load      - Run load tests"
	@echo "  make test-stress    - Run stress tests"
	@echo ""
	@echo "Code Quality:"
	@echo "  make fmt            - Format code"
	@echo "  make vet            - Run go vet"
	@echo "  make lint           - Format and vet"
	@echo ""
	@echo "Docker:"
	@echo "  make docker-build   - Build Docker image"
	@echo "  make docker-up      - Start cluster"
	@echo "  make docker-down    - Stop cluster"
	@echo "  make docker-logs    - View cluster logs"
	@echo ""
	@echo "Development:"
	@echo "  make proto          - Regenerate proto files"
	@echo ""
	@echo "Usage examples:"
	@echo "  ./bin/mimorictl get key"
	@echo "  ./bin/mimorictl put key value"
	@echo "  ./bin/mimorictl --addr localhost:4000 status"

