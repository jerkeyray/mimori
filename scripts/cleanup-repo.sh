#!/bin/bash
# Script to clean up repository structure

set -e

echo "🧹 Cleaning up Mimori repository..."

# Remove committed binaries
echo "Removing committed binaries..."
git rm -f mimorictl mimorid 2>/dev/null || true

# Remove duplicate proto files at root
echo "Removing duplicate proto files at root..."
git rm -f raft_grpc.pb.go raft.pb.go 2>/dev/null || true

# Remove empty directories
echo "Removing empty directories..."
[ -d web ] && rmdir web 2>/dev/null || true
[ -d internal/config ] && rmdir internal/config 2>/dev/null || true
[ -d internal/node ] && rmdir internal/node 2>/dev/null || true

# Remove data directories (they're gitignored now)
echo "Removing local data directories..."
rm -rf data-dashboard/ data-spawn/ 2>/dev/null || true

# Clean build cache
echo "Cleaning build cache..."
rm -rf .gocache/ 2>/dev/null || true

# Format code
echo "Formatting code..."
go fmt ./...

# Run go mod tidy
echo "Tidying go.mod..."
go mod tidy

echo "✅ Cleanup complete!"
echo ""
echo "Next steps:"
echo "1. Review changes: git status"
echo "2. Commit changes: git add -A && git commit -m 'Clean up repository structure'"
echo "3. Push to remote: git push"
