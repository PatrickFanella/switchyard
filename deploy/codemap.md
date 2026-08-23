# deploy/

## Responsibility
Installs the Docker Compose stack as a host-managed systemd service and defines the pinned Gitea Actions job image used by Switchyard verification.

## Design
- `switchyard.service` is a template with repository/user/group placeholders.
- `install-systemd.sh` requires root, validates Docker/Compose inputs, renders atomically through a temporary file, and enables the unit.
- `runner/Dockerfile` composes the digest-pinned Gitea runner image with the exact Go toolchain required by CI; `runner/README.md` documents publishing and label configuration.
- The installer removes a legacy standalone llama-line service during native cutover.
- Backup service/timer templates are installed alongside the runtime unit and enabled only through an explicit opt-in with a configured backup destination.
- Restore-drill service/timer templates are rendered from the validated policy calendar and bounded RTO. They are enabled only with `SWITCHYARD_INSTALL_RESTORE_DRILL_TIMER=1`; all probes precede mutation, and deadline initialization occurs immediately before timer enable with rollback on failure.
- Starter monitoring rules cover canonical model-ingress availability, broker
  backlog, worker failures, and backup freshness. The API republishes the
  backup and restore-drill protected textfiles on `/metrics`; external rules alert on a failed or overdue drill.

## Flow
1. Resolve repository and run identity.
2. Render/install `/etc/systemd/system/switchyard.service`.
3. Disable legacy llama-line if present.
4. Preflight restore policy, private directories, Docker, and policy calendar when restore scheduling is explicitly requested.
5. Install backup and restore-drill templates; enable each timer only through its own opt-in.
6. Reload systemd, enable/start Switchyard, and print status.

## Integration
The unit runs the root `docker-compose.yml`; deployment therefore inherits Compose environment and port safety defaults.
