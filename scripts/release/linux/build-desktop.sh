#!/usr/bin/env bash
set -euo pipefail
[[ $# -eq 2 ]] || { echo 'usage: build-desktop.sh <version> <output>' >&2; exit 2; }
version=$1
output=$2
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && ! -e "$output" ]]
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]]
repository=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd -P)
node "$repository/release/shared/sidecar/build.mjs" --host
corepack pnpm --dir "$repository/frontend" --filter @go-admin-plus/admin-desktop build
corepack pnpm --dir "$repository/frontend" --filter @go-admin-plus/admin-desktop exec tauri build --features custom-protocol --bundles deb,appimage --config "{\"version\":\"$version\"}"
mkdir -p "$output"
cp "$repository"/frontend/apps/admin-desktop/src-tauri/target/release/bundle/deb/*.deb "$output/"
cp "$repository"/frontend/apps/admin-desktop/src-tauri/target/release/bundle/appimage/*.AppImage "$output/"
cp "$repository/release/linux/DESKTOP-INSTALL.md" "$output/INSTALL.md"
node --input-type=module - "$version" "$output" "${SOURCE_SHA:-}" <<'JS'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
const [version, output, sourceSha] = process.argv.slice(2)
writeFileSync(join(output, 'provenance.json'), JSON.stringify({ schemaVersion: 1, product: 'go-admin-plus', version, sourceSha: sourceSha || null, platform: 'linux-x64-desktop', releaseClass: 'private-release', signed: false, remotePublished: false }, null, 2) + '\n')
JS
(cd "$output" && sha256sum ./*.deb ./*.AppImage INSTALL.md provenance.json > SHA256SUMS)
