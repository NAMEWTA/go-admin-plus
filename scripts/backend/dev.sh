#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

target=${1:-}
profile=${2:-server-sqlite}

case $target in
  server|server-sqlite|server-postgres)
    require_tool go
    require_pnpm
    test "$target" = server || profile=$target
    config_file=$(profile_config "$profile")
    cd "$backend_root"
    set -- serve --development --profile "$profile" --data-root "$repo_root/.data/server"
    test -z "$config_file" || set -- "$@" --config "$config_file"
    if test "$profile" = server-sqlite; then
      set -- "$@" --sqlite-path "$(sqlite_path)" --with-worker
    fi
    exec go run ./cmd/server "$@"
    ;;
  web)
    exec "$repo_root/scripts/frontend/dev.sh" web
    ;;
  desktop)
    exec "$repo_root/scripts/frontend/dev.sh" desktop
    ;;
  *)
    fail "unsupported development target: $target (expected server, web, or desktop)"
    ;;
esac
