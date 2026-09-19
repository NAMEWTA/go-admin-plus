#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)

fail() {
  printf '%s\n' "task contract: $*" >&2
  exit 1
}

task_command=$(command -v task 2>/dev/null) || fail 'Go Task is required to verify the command contract'
node_command=$(command -v node 2>/dev/null) || fail 'Node.js is required to verify the command contract'
required_task_version=3.48.0
task_version=$("$task_command" --version 2>/dev/null) || fail 'Go Task version cannot be determined'
test "$task_version" = "$required_task_version" ||
  fail "Go Task $required_task_version is required; found $task_version"

"$node_command" --test "$repo_root/scripts/frontend/common.test.mjs" || exit 1

task_list=$("$task_command" --list --json)
printf '%s' "$task_list" | "$node_command" -e '
  const { tasks } = JSON.parse(require("node:fs").readFileSync(0, "utf8"))
  const actual = tasks.map(({ name }) => name).sort()
  const expected = [
    "architecture:check", "build", "compatibility:zero", "contract:lint", "default",
    "dev", "doctor", "docs:check", "generate", "generate:check", "governance:check", "lint",
    "lint:staged", "migrate", "package", "release", "release:verify", "task:contract", "test", "worker",
  ].sort()
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    console.error(`task contract: public tasks differ\nexpected=${expected}\nactual=${actual}`)
    process.exit(1)
  }
' || exit 1

hook=$repo_root/.husky/pre-commit
test -f "$hook" || fail 'missing root pre-commit hook'
grep -Eq '(^|[[:space:]])task[[:space:]]+lint:staged([[:space:]]|$)' "$hook" ||
  fail 'pre-commit hook does not delegate to task lint:staged'

if ( . "$repo_root/scripts/backend/common.sh"; require_tool go-admin-contract-missing-tool ) \
  >/dev/null 2>&1; then
  fail 'managed scripts do not propagate a missing-tool failure'
fi

shell_command=$(command -v sh 2>/dev/null) || fail 'POSIX sh is required to verify the Hook contract'
hook_status=0
PATH=/nonexistent "$shell_command" "$hook" >/dev/null 2>&1 || hook_status=$?
test "$hook_status" -ne 0 || fail 'pre-commit hook hides a missing Task CLI failure'

for task_name in dev build test lint generate migrate package worker doctor; do
  dry_output=$("$task_command" --verbose --dry "$task_name" 2>&1) ||
    fail "$task_name cannot resolve from the repository root with default inputs"
  case $dry_output in
    *run-script.mjs*scripts/*) ;;
    *) fail "$task_name does not delegate through the managed script runner" ;;
  esac
done
release_dry=$("$task_command" --verbose --dry release VERSION=0.0.0-contract 2>&1) ||
  fail 'release cannot resolve from the repository root with an explicit version'
case $release_dry in
  *run-script.mjs*scripts/*) ;;
  *) fail 'release does not delegate through the managed script runner' ;;
esac

unknown_status=0
"$task_command" __contract_unknown_task__ >/dev/null 2>&1 || unknown_status=$?
test "$unknown_status" -ne 0 || fail 'unknown root tasks do not fail'

child_status=0
child_output=$("$task_command" task:failure-probe 2>&1) || child_status=$?
test "$child_status" -ne 0 || fail 'managed child failures do not propagate through Task'
case $child_output in
  *'intentional task contract failure'*) ;;
  *) fail 'failure probe did not reach the managed child script' ;;
esac

runner_status=0
runner_output=$(GO_ADMIN_POSIX_SHELL=go-admin-missing-shell \
  "$node_command" "$repo_root/scripts/backend/run-script.mjs" \
  scripts/backend/failure-probe.sh 2>&1) || runner_status=$?
test "$runner_status" -eq 127 || fail 'missing POSIX shell does not return exit code 127'
case $runner_output in
  *'required POSIX shell is not installed'*) ;;
  *) fail 'missing POSIX shell does not return a deterministic diagnostic' ;;
esac

GO_ADMIN_ARTIFACTS_DIR='.artifacts/path with spaces' \
GO_ADMIN_SQLITE_PATH='.data/path with spaces/product.sqlite3' \
  "$shell_command" "$repo_root/scripts/backend/path-contract.sh"
GO_ADMIN_ARTIFACTS_DIR='.artifacts/path with spaces' \
  "$shell_command" "$repo_root/scripts/frontend/path-contract.sh"

printf '%s\n' 'TASK_CONTRACT_PASS'
