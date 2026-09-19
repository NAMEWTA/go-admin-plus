#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

target=${1:-}
case $target in
  web)
    require_pnpm
    web_stage=$artifacts_root/package-staging/web
    web_dist=$web_stage/dist
    package_output=$artifacts_root/packages/go-admin-web.tar.gz
    package_tmp=$package_output.tmp
    mkdir -p "$web_stage" "$artifacts_root/packages"
    cd "$frontend_root"
    GO_ADMIN_BUILD_DIR="$web_dist" run_pnpm --filter @go-admin-plus/admin-web build
    trap 'rm -f "$package_tmp"' EXIT HUP INT TERM
    tar -C "$web_stage" -czf "$package_tmp" dist
    mv "$package_tmp" "$package_output"
    trap - EXIT HUP INT TERM
    ;;
  desktop)
    require_pnpm
    require_tool go
    require_desktop_workspace
    case $(go env GOHOSTOS) in
      darwin) desktop_bundle=app ;;
      windows) desktop_bundle=nsis ;;
      linux) desktop_bundle=deb,appimage ;;
      *) fail 'unsupported desktop packaging host' ;;
    esac
    cd "$frontend_root"
    node "$repo_root/release/shared/sidecar/build.mjs" --host
    exec_pnpm --filter @go-admin-plus/admin-desktop tauri build \
      --features custom-protocol --bundles "$desktop_bundle"
    node "$frontend_root/apps/admin-desktop/scripts/verify-build.mjs"
    ;;
  *)
    fail "unsupported frontend package target: $target (expected web or desktop)"
    ;;
esac
