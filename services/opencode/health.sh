#!/usr/bin/env sh
set -eu

. /usr/local/libexec/switchyard-opencode-password
load_opencode_password

exec node /usr/local/libexec/switchyard-opencode-health.mjs
