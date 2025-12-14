.PHONY: build install clean test help

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

# Run specific test
test-follower-reads:
	go test ./tests -v -run TestFollowerReads

# Help
help:
	@echo "Available targets:"
	@echo "  make build          - Build binaries to ./bin/"
	@echo "  make install        - Install binaries to GOPATH/bin"
	@echo "  make clean          - Remove build artifacts"
	@echo "  make test           - Run all tests"
	@echo "  make test-e2e       - Run e2e tests"
	@echo "  make test-follower-reads - Run follower reads test"
	@echo ""
	@echo "Usage examples:"
	@echo "  ./bin/mimorictl get key"
	@echo "  ./bin/mimorictl put key value"
	@echo "  ./bin/mimorictl --addr localhost:4000 status"

