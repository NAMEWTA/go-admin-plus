#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

expected_artifacts="$repo_root/.artifacts/path with spaces"
test "$artifacts_root" = "$expected_artifacts" ||
  fail 'relative GO_ADMIN_ARTIFACTS_DIR is not resolved from the repository root'
