# deploy/

## Responsibility
Installs the Docker Compose stack as a host-managed systemd service.

## Design
- `switchyard.service` is a template with repository/user/group placeholders.
- `install-systemd.sh` requires root, validates Docker/Compose inputs, renders atomically through a temporary file, and enables the unit.
- The installer removes a legacy standalone llama-line service during native cutover.

## Flow
1. Resolve repository and run identity.
2. Render/install `/etc/systemd/system/switchyard.service`.
3. Disable legacy llama-line if present.
4. Reload systemd, enable/start Switchyard, and print status.

## Integration
The unit runs the root `docker-compose.yml`; deployment therefore inherits Compose environment and port safety defaults.
