#!/usr/bin/env bash

set -e

# Navigate to the root of the project
cd "$(dirname "$0")/.."

echo "==> Running go fmt..."
go fmt ./...

echo "==> Running golangci-lint..."
# Note: assumes golangci-lint is installed locally
golangci-lint run ./...

echo "==> Running markdownlint..."
npx markdownlint-cli2 "**/*.md" 2>/dev/null

echo "==> Running go test..."
go test -v -race -coverprofile=coverage.out -count=1 ./...

echo "==> Coverage summary:"
go tool cover -func=coverage.out | tail -n 1
