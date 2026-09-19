#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
. "$repo_root/scripts/backend/common.sh"

cd "$repo_root"
require_tool node
require_pnpm

case ${1:-generate} in
  verify)
    node scripts/contracts/cli.mjs lint
    node --test --test-concurrency=1 scripts/contracts/*.test.mjs
    run_pnpm --dir frontend --filter @go-admin-plus/api-client typecheck
    exec_pnpm --dir frontend --filter @go-admin-plus/api-client test
    ;;
  lint)
    exec node scripts/contracts/cli.mjs lint
    ;;
  generate)
    test "$#" -eq 0 || shift
    exec node scripts/contracts/cli.mjs generate "$@"
    ;;
  *)
    fail "unsupported contract action: ${1:-}"
    ;;
esac
