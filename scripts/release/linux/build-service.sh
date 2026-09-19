#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 3 ]] || { echo "usage: build-service.sh <version> <source-sha> <output-directory>" >&2; exit 2; }
version=$1
source_sha=$2
output=$3
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || { echo "invalid release version" >&2; exit 2; }
[[ "$source_sha" =~ ^[0-9a-f]{40}$ ]] || { echo "source SHA must be lowercase and exact" >&2; exit 2; }
[[ ! -e "$output" ]] || { echo "output directory already exists" >&2; exit 1; }
command -v go >/dev/null
command -v tar >/dev/null
command -v sha256sum >/dev/null

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repository=$(cd -- "$script_dir/../../.." && pwd -P)
backend="$repository/backend"
stage=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/go-admin-plus-service.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT

for architecture in amd64 arm64; do
  package_root="$stage/go-admin-plus-$version-linux-$architecture"
  mkdir -p "$package_root/bin" "$package_root/config" "$package_root/systemd"
  (cd "$backend" && GOOS=linux GOARCH="$architecture" CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' \
    -o "$package_root/bin/go-admin-plus-server" ./cmd/server)
  cp "$repository/deploy/compose/config/server-sqlite.yaml" "$package_root/config/"
  cp "$repository/deploy/compose/config/server-postgres.yaml" "$package_root/config/"
  cp "$repository/release/linux/go-admin-plus-server.service" "$package_root/systemd/"
  cp "$repository/release/linux/go-admin-plus-server-postgres.service" "$package_root/systemd/"
  cp "$repository/release/linux/go-admin-plus-worker-postgres.service" "$package_root/systemd/"
  cp "$repository/release/linux/SERVER-INSTALL.md" "$package_root/"
  tar -C "$stage" -czf "$stage/go-admin-plus-server-$version-linux-$architecture.tar.gz" "$(basename "$package_root")"
done

mkdir -p "$output"
mv "$stage"/*.tar.gz "$output/"
(cd "$output" && sha256sum ./*.tar.gz > SHA256SUMS)
echo "GO_ADMIN_LINUX_SERVICE_BUILD_PASS"
