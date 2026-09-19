#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

require_tool go
require_pnpm
cd "$backend_root"
go test ./... -count=1 -timeout=25m
exec go test -tags sqlite ./... -count=1 -timeout=25m
