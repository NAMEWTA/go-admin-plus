#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 2 ]] || { echo "usage: verify-install.sh <dmg> <evidence-directory>" >&2; exit 2; }
dmg=$1
evidence=$2
[[ -f "$dmg" && ! -e "$evidence" ]]
[[ "${CI:-}" == true && "${GITHUB_ACTIONS:-}" == true && -n "${RUNNER_TEMP:-}" ]]
sandbox_created=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/go-admin-macos-install.XXXXXX")
sandbox=$(cd -- "$sandbox_created" && pwd -P)
mountpoint="$sandbox/mount"
mkdir -p -- "$mountpoint" "$sandbox/Applications" "$sandbox/tmp" "$evidence"
install_root="$sandbox/Applications/Go Admin Plus.app"
data_root="$HOME/Library/Application Support/com.goadmin.plus/data"
config_file="$HOME/Library/Application Support/com.goadmin.plus/connection.json"
log_root="$HOME/Library/Logs/com.goadmin.plus"
keyring_service=com.goadmin.plus.stronghold
keyring_account=desktop-session-vault
[[ ! -e "$data_root" && ! -e "$log_root" && ! -e "$config_file" ]]
if security find-generic-password -s "$keyring_service" -a "$keyring_account" >/dev/null 2>&1; then
  echo "desktop verification credential already exists" >&2
  exit 1
fi
mounted=false
child=
cleanup() {
  if [[ -n "$child" ]] && kill -0 "$child" 2>/dev/null; then kill "$child" 2>/dev/null || true; fi
  if [[ "$mounted" == true ]]; then hdiutil detach -quiet "$mountpoint" || true; fi
  security delete-generic-password -s "$keyring_service" -a "$keyring_account" >/dev/null 2>&1 || true
  if [[ "$data_root" == "$HOME/Library/Application Support/com.goadmin.plus/data" && "$log_root" == "$HOME/Library/Logs/com.goadmin.plus" ]]; then
    rm -rf -- "$data_root" "$log_root"
    rm -f -- "$config_file"
  fi
  rm -rf -- "$sandbox"
}
trap cleanup EXIT
hdiutil attach -quiet -readonly -nobrowse -mountpoint "$mountpoint" "$dmg"
mounted=true
source_app=$(find "$mountpoint" -maxdepth 1 -type d -name '*.app' -print -quit)
[[ -n "$source_app" ]]
installed="$sandbox/Applications/Go Admin Plus.app"
ditto "$source_app" "$installed"
executable=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$installed/Contents/Info.plist")
launch_and_stop() {
  local phase=$1
  TMPDIR="$sandbox/tmp" "$installed/Contents/MacOS/$executable" > "$evidence/launch-$phase.log" 2>&1 &
  child=$!
  for _ in {1..60}; do
    if ! kill -0 "$child" 2>/dev/null; then echo "installed app exited during $phase smoke" >&2; exit 1; fi
    if [[ -s "$data_root/go-admin-plus.db" && -f "$data_root/session.stronghold" ]]; then break; fi
    sleep 1
  done
  [[ -s "$data_root/go-admin-plus.db" && -f "$data_root/session.stronghold" ]]
  sleep 3
  kill -0 "$child"
  security find-generic-password -s "$keyring_service" -a "$keyring_account" >/dev/null
  kill -TERM "$child"
  wait "$child" || true
  child=
}
mkdir -p -- "$(dirname -- "$config_file")"
printf '%s\n' '{"mode":"local","serverUrl":"","caCertificate":""}' > "$config_file"
launch_and_stop first-run
database_identity=$(stat -f '%d:%i' "$data_root/go-admin-plus.db")
launch_and_stop restart
test "$(stat -f '%d:%i' "$data_root/go-admin-plus.db")" = "$database_identity"
printf '{"schemaVersion":1,"installRoot":"%s","dataRoot":"%s","logRoot":"%s","firstLaunch":"passed","restart":"passed","dataPathStable":true}\n' \
  "$install_root" "$data_root" "$log_root" > "$evidence/result.json"
echo "GO_ADMIN_MACOS_INSTALL_PASS" | tee "$evidence/result.log"
