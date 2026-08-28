#!/usr/bin/env sh
set -eu

. /usr/local/libexec/switchyard-opencode-password
load_opencode_password

exec node -e "const u=process.env.OPENCODE_SERVER_USERNAME,p=process.env.OPENCODE_SERVER_PASSWORD,d=process.env.OPEN_PILOT_WORKTREE_ROOT; fetch('http://127.0.0.1:4096/session?directory='+encodeURIComponent(d),{headers:{authorization:'Basic '+Buffer.from(u+':'+p).toString('base64')}}).then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"
