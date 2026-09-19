#!/bin/sh

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
frontend_root=$repo_root/frontend

fail() {
  printf '%s\n' "frontend task: $*" >&2
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

require_desktop_workspace() {
  test -f "$frontend_root/apps/admin-desktop/src-tauri/tauri.conf.json" ||
    fail 'Tauri 2 desktop workspace is not available'
}
