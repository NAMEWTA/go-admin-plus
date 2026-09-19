#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

require_pnpm
cd "$frontend_root"
exec_pnpm exec lint-staged
