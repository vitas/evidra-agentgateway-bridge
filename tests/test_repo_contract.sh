#!/usr/bin/env bash
set -euo pipefail

for required in \
  Makefile .golangci.yml .github/workflows/ci.yml Dockerfile \
  examples/compose.yaml examples/Dockerfile.agentgateway-test \
  tests/e2e_otlp.sh testdata/expected/otlp-stats.json; do
  test -s "$required" || { echo "missing $required" >&2; exit 1; }
done

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
