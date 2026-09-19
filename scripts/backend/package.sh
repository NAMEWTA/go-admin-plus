#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

target=${1:-}
profile=${2:-server-sqlite}

case $target in
  server|server-sqlite|server-postgres)
    require_tool go
    test "$target" = server || profile=$target
    validate_profile "$profile"
    os=$(go env GOOS)
    arch=$(go env GOARCH)
    mkdir -p "$artifacts_root/packages"
    exec sh -c 'cd "$1" && go build -trimpath -o "$2/go-admin-plus-$3-$4" ./cmd/server' sh \
      "$backend_root" "$artifacts_root/packages" "$os" "$arch"
    ;;
  web|desktop)
    exec "$repo_root/scripts/frontend/package.sh" "$target"
    ;;
  *)
    fail "unsupported package target: $target (expected server, web, or desktop)"
    ;;
esac
