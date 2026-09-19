#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

require_pnpm
cd "$frontend_root"
run_pnpm lint
exec_pnpm type-check
