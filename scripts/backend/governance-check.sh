#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

required_files='
.gitignore
.gitattributes
.editorconfig
Taskfile.yml
.github/ISSUE_TEMPLATE/bug-report.yml
.github/ISSUE_TEMPLATE/feature-request.yml
.github/PULL_REQUEST_TEMPLATE.md
.husky/pre-commit
deploy/README.md
release/README.md
database/README.md
'

for relative_path in $required_files; do
  test -f "$repo_root/$relative_path" || fail "missing root-owned governance asset: $relative_path"
done

for relative_path in scripts/backend scripts/frontend; do
  test -d "$repo_root/$relative_path" || fail "missing root-owned script directory: $relative_path"
done

actual=$(mktemp "${TMPDIR:-/tmp}/go-admin-governance.XXXXXX")
trap 'rm -f "$actual"' EXIT HUP INT TERM

find "$repo_root" \
  \( -path "$repo_root/.git" -o -path "$repo_root/.github" -o -path "$repo_root/.husky" -o \
    -path "$repo_root/.artifacts" -o -path "$repo_root/.data" -o \
    -path "$repo_root/specdev-worktree" -o -path "$repo_root/specdev-candidate" -o \
    -path "$repo_root/speculo" -o -name node_modules -o -name dist -o -name target -o -name coverage \) -prune -o \
  -type f \( -path '*/.github/*' -o -path '*/.husky/*' -o \
    -name .gitignore -o -name .gitattributes -o -name .editorconfig -o \
    -name .dockerignore -o -name Makefile -o -name 'Dockerfile*' -o \
    -name 'docker-compose*.yml' -o -name 'docker-compose*.yaml' \) \
  ! -path "$repo_root/.gitignore" ! -path "$repo_root/.gitattributes" \
  ! -path "$repo_root/.editorconfig" ! -path "$repo_root/.dockerignore" \
  ! -path "$repo_root/Makefile" ! -path "$repo_root/Dockerfile" \
  ! -path "$repo_root/docker-compose.yml" ! -path "$repo_root/docker-compose.yaml" \
  -print >"$actual"

if test -s "$actual"; then
  sed "s|^$repo_root/||" "$actual" >&2
  fail 'nested governance assets must be owned by the repository root'
fi

printf '%s\n' 'GOVERNANCE_CHECK_PASS'
