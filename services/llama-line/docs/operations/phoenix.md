# Phoenix Operations

Phoenix runs on almaz and receives OTLP HTTP traces from kvant llama-line.

## Live endpoint

- LAN UI/OTLP: `http://10.0.0.200:6006`
- OTLP HTTP: `http://10.0.0.200:6006/v1/traces`
- Authelia-protected UI: `https://phoenix.subcult.tv`

## Current hardening

The repo compose mirror in `docker/phoenix.yml` should keep:

```yaml
image: arizephoenix/phoenix:version-16.0.0
ports:
  - "10.0.0.200:6006:6006"
environment:
  PHOENIX_WORKING_DIR: /mnt/data
  PHOENIX_DEFAULT_RETENTION_POLICY_DAYS: "60"
  PHOENIX_TELEMETRY_ENABLED: "false"
  PHOENIX_ALLOW_EXTERNAL_RESOURCES: "false"
```

## Data backup options

Stop-the-world Docker volume tarball:

```bash
ssh almaz 'docker stop phoenix && docker run --rm -v monitoring_phoenix_data:/data -v /opt/server/monitoring/backups:/backup alpine:3.20 sh -c "tar -czf /backup/phoenix-data-$(date -u +%Y%m%dT%H%M%SZ).tar.gz -C /data ." && docker start phoenix'
```

Lower-downtime SQLite backup inside the mounted data directory:

```bash
ssh almaz 'docker exec phoenix sqlite3 /mnt/data/phoenix.db ".backup /mnt/data/phoenix-backup.db"'
```

## Auth stance

Phoenix native auth remains disabled. Public UI is protected by Authelia. LAN direct `10.0.0.200:6006` is trusted LAN only.

If this changes, enable Phoenix auth and create a system API key before turning on automation that requires authenticated API calls.

## Project routing

Phoenix OTLP project selection uses the `x-project-name` HTTP header first and `openinference.project.name` resource attribute as fallback.

Current static broker routing sends to `llama-line`. Per-client routing should send traces to Phoenix projects named after llama-line client keys, e.g. `subcorp`, `edda`, `jury-rigged`, `augr`, `paqr`.
