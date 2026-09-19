#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 5 ]] || { echo "usage: emit-artifacts.sh <version> <source-sha> <app> <dmg> <output>" >&2; exit 2; }
version=$1
source_sha=$2
app=$3
dmg=$4
output=$5
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]
[[ "$source_sha" =~ ^[0-9a-f]{40}$ ]]
[[ -d "$app" && -f "$dmg" && ! -e "$output" ]]
command -v jq >/dev/null
command -v syft >/dev/null
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repository=$(cd -- "$script_dir/../../.." && pwd -P)
artifact_arch=$(uname -m)
[[ "$artifact_arch" == x86_64 ]] && artifact_arch=x64
mkdir -p -- "$output/sbom"
ditto -c -k --keepParent "$app" "$output/go-admin-plus-macos-$artifact_arch.app.zip"
cp "$dmg" "$output/"
cp "$repository/release/macos/identity.json" "$repository/release/macos/INSTALL.md" "$output/"
syft "dir:$app" -o "spdx-json=$output/sbom/go-admin-plus-macos-$artifact_arch.spdx.json"
jq -n --arg version "$version" --arg sourceSha "$source_sha" --arg arch "$artifact_arch" \
  '{schemaVersion:1,product:"go-admin-plus",version:$version,sourceSha:$sourceSha,platform:("macos-"+$arch),architectures:[$arch],releaseClass:"private-release",signed:false,notarized:false,remotePublished:false,sbomFormat:"SPDX JSON"}' \
  > "$output/provenance.json"
(cd -- "$output" && find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 shasum -a 256 > SHA256SUMS)
(cd -- "$output" && shasum -a 256 -c SHA256SUMS)
echo "GO_ADMIN_MACOS_SUPPLY_CHAIN_PASS"
