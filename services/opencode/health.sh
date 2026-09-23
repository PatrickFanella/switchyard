#!/usr/bin/env sh
set -eu

. /usr/local/libexec/switchyard-opencode-password
load_opencode_password

exec node -e "const u=process.env.OPENCODE_SERVER_USERNAME,p=process.env.OPENCODE_SERVER_PASSWORD; fetch('http://127.0.0.1:4096/global/health',{headers:{authorization:'Basic '+Buffer.from(u+':'+p).toString('base64')},signal:AbortSignal.timeout(4000)}).then(async r=>{if(!r.ok)process.exit(1);const body=await r.json();process.exit(body&&body.healthy===true?0:1)}).catch(()=>process.exit(1))"
