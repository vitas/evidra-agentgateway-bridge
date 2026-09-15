#!/usr/bin/env bash
set -euo pipefail

for required in Makefile .golangci.yml .github/workflows/ci.yml Dockerfile; do
  test -s "$required" || { echo "missing $required" >&2; exit 1; }
done

grep -Fq 'Status: experimental' README.md
grep -Fq 'make lint' .github/workflows/ci.yml
grep -Fq 'go test -race ./...' .github/workflows/ci.yml
