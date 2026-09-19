#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

action=${1:-}
version=${2:-}
require_tool node

case $action in
  preflight)
    test -n "$version" || fail 'release version is required'
    root_sha=$(git -C "$repo_root" rev-parse HEAD)
    node "$repo_root/release/manifest/product-release.mjs" preflight \
      --version "$version" --root-ref "$root_sha"
    node "$repo_root/release/manifest/scan-compatibility.mjs"
    exec node --test "$repo_root/release/manifest/product-release.test.mjs"
    ;;
  verify)
    node "$repo_root/scripts/quality/compatibility-zero.mjs"
    exec node --test \
      "$repo_root/release/manifest/product-release.test.mjs" \
      "$repo_root/release/shared/sidecar/build.test.mjs" \
      "$repo_root/scripts/release/linux/verify-policy.test.mjs" \
      "$repo_root/scripts/release/macos/verify-policy.test.mjs" \
      "$repo_root/scripts/release/windows/verify-policy.test.mjs"
    ;;
  *)
    fail "unsupported release action: $action"
    ;;
esac
