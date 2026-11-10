#!/usr/bin/env bash
# Run from any working directory. PostgreSQL coverage uses a dedicated test DB only.
set -euo pipefail
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$script_dir/.."
go test ./...
go vet ./...
if [[ -n "${TEST_DATABASE_URL:-}" ]]; then
 go test -race ./internal/dbbridge -run 'TestControl|TestWorkflowDatabaseLock' -count=1
else
 echo 'PostgreSQL tests skipped: set TEST_DATABASE_URL to a dedicated test database.'
fi
cd ../agent/framework
go test -race ./...
go vet ./...
