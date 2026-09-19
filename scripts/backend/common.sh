#!/bin/sh

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
backend_root=$repo_root/backend

fail() {
  printf '%s\n' "go-admin-plus task: $*" >&2
  exit 1
}

require_tool() {
  command -v "$1" >/dev/null 2>&1 || fail "required tool is not installed: $1"
}

resolve_repo_path() {
  case $1 in
    /*|[A-Za-z]:[\\/]*) printf '%s\n' "$1" ;;
    *) printf '%s/%s\n' "$repo_root" "$1" ;;
  esac
}

artifacts_root=$(resolve_repo_path "${GO_ADMIN_ARTIFACTS_DIR:-.artifacts}")
. "$repo_root/scripts/backend/pnpm.sh"

validate_profile() {
  case $1 in
    server-sqlite|server-postgres) ;;
    *) fail "unsupported profile: $1 (expected server-sqlite or server-postgres)" ;;
  esac
}

profile_config() {
  validate_profile "$1"
  test -n "${GO_ADMIN_CONFIG_FILE:-}" || return 0
  config_file=$(resolve_repo_path "$GO_ADMIN_CONFIG_FILE")
  test -r "$config_file" || fail 'GO_ADMIN_CONFIG_FILE does not name a readable YAML file'
  printf '%s\n' "$config_file"
}

sqlite_path() {
  resolve_repo_path "${GO_ADMIN_SQLITE_PATH:-.data/server/go-admin-plus.sqlite3}"
}
