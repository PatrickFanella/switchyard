# deploy/

## Responsibility
Installs the Docker Compose stack as a host-managed systemd service and defines the pinned Gitea Actions job image used by Switchyard verification.

## Design
- `switchyard.service` is a template with repository/user/group placeholders.
- `install-web-gate.sh` preflights renderer, Compose, and nft syntax; atomically installs the renderer root-owned at `/usr/lib/switchyard`; stops a running web container before a changed gate; and restores the prior renderer, rules/table, files, and unit state on failure without reopening web.
- `install-systemd.sh` requires the installed web gate active before mutation, resolves one absolute non-symlink env file, and restores all five managed unit files, their enablement/activation, and the Compose workload after a failed install.
- `runner/Dockerfile` composes the digest-pinned Gitea runner image with the exact Go toolchain required by CI; `runner/README.md` documents publishing and label configuration.
- The installer removes a legacy standalone llama-line service during native cutover.
- Backup service/timer templates are installed alongside the runtime unit and enabled only through an explicit opt-in with a configured backup destination.
- Restore-drill service/timer templates are rendered from the validated policy calendar and bounded RTO. They are enabled only with `SWITCHYARD_INSTALL_RESTORE_DRILL_TIMER=1`; all probes precede mutation, and deadline initialization occurs immediately before timer enable with rollback on failure.
- Starter monitoring rules cover canonical model-ingress availability, broker
  backlog, worker failures, and backup freshness. The API republishes the
  backup and restore-drill protected textfiles on `/metrics`; external rules alert on a failed or overdue drill.

## Flow
1. Resolve the production env path, install the root-owned renderer, and render/install `/etc/switchyard/web-gate.nft` and `switchyard-web-gate.service`; a changed live gate first stops Compose `web`.
2. Require the gate active, then resolve repository and run identity and install `switchyard.service`.
3. Disable legacy llama-line if present.
4. Preflight restore policy, private directories, Docker, and policy calendar when restore scheduling is explicitly requested.
5. Install backup and restore-drill templates; enable each timer only through its own opt-in.
6. Reload systemd, enable/start Switchyard, and print status.

## Integration
The unit runs the root `docker-compose.yml`; deployment therefore inherits Compose environment and port safety defaults.
