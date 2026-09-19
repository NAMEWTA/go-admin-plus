#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

profile=${1:-server-postgres}
require_tool go
config_file=$(profile_config "$profile")
cd "$backend_root"
set -- worker --profile "$profile" --data-root "$repo_root/.data/server"
test -z "$config_file" || set -- "$@" --config "$config_file"
if test "$profile" = server-sqlite; then
  set -- "$@" --sqlite-path "$(sqlite_path)"
fi
exec go run ./cmd/server "$@"
