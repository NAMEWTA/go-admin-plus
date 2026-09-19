#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 3 ]] || { echo "usage: build.sh <version> <build-number> <output-app>" >&2; exit 2; }
version=$1
build_number=$2
output_app=$3
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]
[[ "$build_number" =~ ^[1-9][0-9]*$ ]]
[[ "$(uname -s)" == Darwin ]]
command -v rustup >/dev/null
command -v pnpm >/dev/null
[[ ! -e "$output_app" ]] || { echo "output app already exists" >&2; exit 1; }

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repository=$(cd -- "$script_dir/../../.." && pwd -P)
tauri_root="$repository/frontend/apps/admin-desktop/src-tauri"
binaries="$tauri_root/binaries"
case "$(uname -m)" in
 arm64) target=aarch64-apple-darwin; artifact_arch=aarch64 ;;
 x86_64) target=x86_64-apple-darwin; artifact_arch=x64 ;;
 *) echo "unsupported macOS architecture" >&2; exit 1 ;;
esac
rustup target list --installed | grep -Fx "$target" >/dev/null
node "$repository/release/shared/sidecar/build.mjs" --target "$target"
[[ "$(file -b "$binaries/go-admin-sidecar-$target")" == *Mach-O* ]]

pnpm --dir "$repository/frontend" --filter @go-admin-plus/admin-desktop build
pnpm --dir "$repository/frontend" --filter @go-admin-plus/admin-desktop exec tauri build \
  --target "$target" \
  --features custom-protocol \
  --bundles app,dmg \
  --no-sign \
  --config "{\"version\":\"$version\"}"

built_app="$tauri_root/target/$target/release/bundle/macos/Go Admin Plus.app"
built_dmg="$tauri_root/target/$target/release/bundle/dmg/Go Admin Plus_${version}_${artifact_arch}.dmg"
test -d "$built_app"
mkdir -p -- "$(dirname -- "$output_app")"
ditto "$built_app" "$output_app"
"$script_dir/prepare-app.sh" "$output_app" "$version" "$build_number"
rm -f -- "$built_dmg"
"$script_dir/package-dmg.sh" "$output_app" "$built_dmg"
echo "GO_ADMIN_MACOS_BUILD_PASS"
