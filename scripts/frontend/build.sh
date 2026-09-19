#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

require_pnpm

build_web() {
  cd "$frontend_root"
  run_pnpm --filter @go-admin-plus/admin-web build
}

build_desktop() {
  require_tool node
  require_tool go
  require_tool cargo
  require_desktop_workspace
  cd "$frontend_root"
  node "$repo_root/release/shared/sidecar/build.mjs" --host
  run_pnpm --filter @go-admin-plus/admin-desktop tauri build \
    --features custom-protocol --no-bundle
  node "$frontend_root/apps/admin-desktop/scripts/verify-build.mjs"
}

case ${1:-all} in
  all)
    build_web
    build_desktop
    ;;
  web)
    build_web
    ;;
  desktop)
    build_desktop
    ;;
  *)
    fail "unsupported frontend build target: ${1:-} (expected all, web, or desktop)"
    ;;
esac
