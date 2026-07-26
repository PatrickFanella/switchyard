# deploy/

## Responsibility
Installs the Docker Compose stack as a host-managed systemd service and defines the pinned Gitea Actions job image used by Switchyard verification.

## Design
- `switchyard.service` is a template with repository/user/group placeholders.
- `install-systemd.sh` requires root, validates Docker/Compose inputs, renders atomically through a temporary file, and enables the unit.
- `runner/Dockerfile` composes the digest-pinned Gitea runner image with the exact Go toolchain required by CI; `runner/README.md` documents publishing and label configuration.
- The installer removes a legacy standalone llama-line service during native cutover.
- Backup service/timer templates are installed alongside the runtime unit and enabled only through an explicit opt-in with a configured backup destination.
- Starter monitoring rules cover canonical model-ingress availability, broker
  backlog, worker failures, and backup freshness. The API republishes the
  backup timer's protected textfile timestamp on `/metrics` when configured.

## Flow
1. Resolve repository and run identity.
2. Render/install `/etc/systemd/system/switchyard.service`.
3. Disable legacy llama-line if present.
4. Install the backup unit/timer templates; optionally enable the timer after validating `.env`.
5. Reload systemd, enable/start Switchyard, and print status.

## Integration
The unit runs the root `docker-compose.yml`; deployment therefore inherits Compose environment and port safety defaults.
