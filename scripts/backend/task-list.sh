#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

require_tool task
exec task --list
