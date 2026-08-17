#!/usr/bin/env bash
set -euo pipefail

export TESTCONTAINERS_RYUK_DISABLED=true
export VERIFIER_TARGET_URL="${VERIFIER_TARGET_URL:-http://localhost:8200/v1}"
export ASSAY_MODEL="${ASSAY_MODEL:-Qwen/Qwen3-0.6B}"
export ASSAY_PROMPT="${1:-${ASSAY_PROMPT:-What is 2+2?}}"
export TMPDIR="${TMPDIR:-/mnt/disks/data/tmp}"
export GOTMPDIR="${GOTMPDIR:-/mnt/disks/data/gotmp}"
export GOCACHE="${GOCACHE:-/mnt/disks/data/gocache}"

API_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_TEST_CMD="cd $(printf %q "${API_DIR}") && go test -tags integration -run TestBrokerToVerifierToGPU -v -timeout 600s ./inference/integration_test/..."
sg docker -c "${GO_TEST_CMD}"
