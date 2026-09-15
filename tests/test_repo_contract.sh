#!/usr/bin/env bash
set -euo pipefail

for required in \
  Makefile .golangci.yml .github/workflows/ci.yml Dockerfile \
  examples/compose.yaml examples/Dockerfile.agentgateway-test \
  tests/e2e_otlp.sh tests/check_dockerignore.sh .dockerignore \
  testdata/expected/otlp-stats.json; do
  test -s "$required" || { echo "missing $required" >&2; exit 1; }
done

bash tests/check_dockerignore.sh .dockerignore

# Mutation guard: prove the contract rejects a build context that permits dotenv variants.
mutated_dockerignore=$(mktemp)
trap 'rm -f "$mutated_dockerignore"' EXIT
grep -Fvx '.env.*' .dockerignore >"$mutated_dockerignore"
if bash tests/check_dockerignore.sh "$mutated_dockerignore" >/dev/null 2>&1; then
  echo 'dockerignore contract accepted a secret-exposure mutation' >&2
  exit 1
fi

grep -Fq 'Status: experimental' README.md
grep -Fq 'make lint' .github/workflows/ci.yml
grep -Fq 'go test -race ./...' .github/workflows/ci.yml

grep -Fq 'evidra_op:' examples/agentgateway-config.yaml
grep -Fq '/v1/logs' examples/otel-collector-config.yaml
grep -Fq '/v1/traces' examples/otel-collector-config.yaml

grep -Eq '^[[:space:]]{2}agentgateway:' examples/compose.yaml
grep -Eq '^[[:space:]]{2}evidra-bridge:' examples/compose.yaml
grep -Fq 'docker compose -f examples/compose.yaml' README.md
if grep -Fq 'CORR-0' README.md; then
  echo 'README still references unpublished CORR-0 evidence' >&2
  exit 1
fi
