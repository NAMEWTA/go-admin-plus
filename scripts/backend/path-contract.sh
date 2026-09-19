#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

test -f "$backend_root/go.mod" && test -f "$backend_root/cmd/server/main.go" ||
  fail 'backend command root does not resolve to the current Go module'

expected_artifacts="$repo_root/.artifacts/path with spaces"
test "$artifacts_root" = "$expected_artifacts" ||
  fail 'relative GO_ADMIN_ARTIFACTS_DIR is not resolved from the repository root'

expected_sqlite="$repo_root/.data/path with spaces/product.sqlite3"
actual_sqlite=$(sqlite_path)
test "$actual_sqlite" = "$expected_sqlite" ||
  fail 'relative GO_ADMIN_SQLITE_PATH is not resolved from the repository root'
