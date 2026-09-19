#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

require_tool go
cd "$backend_root"
exec go generate ./...
