#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

target=${1:-all}
profile=${2:-server-sqlite}

build_server() {
  require_tool go
  validate_profile "$1"
  mkdir -p "$artifacts_root/build"
  (cd "$backend_root" && go build -trimpath -o "$artifacts_root/build/go-admin-plus" ./cmd/server)
}

case $target in
  all)
    build_server "$profile"
    "$repo_root/scripts/frontend/build.sh" all
    ;;
  server|server-sqlite|server-postgres)
    test "$target" = server || profile=$target
    build_server "$profile"
    ;;
  web|desktop)
    exec "$repo_root/scripts/frontend/build.sh" "$target"
    ;;
  *)
    fail "unsupported build target: $target (expected all, server, web, or desktop)"
    ;;
esac
