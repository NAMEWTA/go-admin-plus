#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

require_tool go
staged_paths=$(git -C "$repo_root" diff --cached --name-only --diff-filter=ACMR)
while IFS= read -r path; do
  case $path in
    backend/*.go|backend/**/*.go)
      formatted=$(gofmt -l "$repo_root/$path")
      if test -n "$formatted"; then
        printf '%s\n' "gofmt is required for: $path" >&2
        exit 1
      fi
      ;;
  esac
done <<EOF
$staged_paths
EOF
