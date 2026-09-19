#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

exec "$repo_root/scripts/contracts/generate.sh"
