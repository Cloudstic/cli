#!/usr/bin/env bash
#
# Local mirror of the CI quality gates (.github/workflows/ci.yml). Keep the
# commands here and the ones in CI in step, so a green run locally means a
# green run on the pull request.

set -euo pipefail

# Navigate to the root of the project
cd "$(dirname "$0")/.."

echo "==> Checking go.mod is tidy..."
go mod tidy
git diff --exit-code -- go.mod go.sum

# Formatting is checked, not applied: .golangci.yml enables the gofmt formatter,
# so `golangci-lint run` reports a misformatted file the same way CI does.
# Run `golangci-lint fmt ./...` to fix what it reports.
# Once per target platform: golangci-lint only analyses the files that build
# for one GOOS, so a single run never opens the *_darwin.go / *_windows.go
# backends under pkg/secretref. CI runs the same three.
for goos in linux darwin windows; do
  echo "==> Running golangci-lint (GOOS=$goos)..."
  GOOS=$goos golangci-lint run ./...
done

echo "==> Running markdownlint..."
npx markdownlint-cli2 "**/*.md"

echo "==> Running govulncheck..."
go run golang.org/x/vuln/cmd/govulncheck@latest ./...

echo "==> Running go test..."
go test -race -shuffle=on -coverprofile=coverage.out -count=1 ./...

echo "==> Coverage summary:"
go tool cover -func=coverage.out | tail -n 1
