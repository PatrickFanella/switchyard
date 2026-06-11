# llama-line Configuration

## Sensitive files

- `config.yaml` contains local admin/client keys and must stay uncommitted unless secrets are redacted.
- `config.example.yaml` is the committed template.
- `.env` contains local project metadata and stays local.

## Add an app/client key

```bash
llama-line add-app --config config.yaml
```

Prompts for name/priority/dedup/cache settings, generates a UUID key, writes it to `keys:`, then asks before `systemctl restart llama-line`.

Use `--no-reload` to only edit config, or `--reload-command "..."` for non-systemd deployments.

## Production-adjacent local defaults

Current kvant runtime uses:

```yaml
database:
  enabled: true
  url: "postgres://llama:line@localhost:5432/llama_line?sslmode=disable"

embeddings:
  enabled: true
  model: qwen3-embedding:0.6b
  dimension: 768

phoenix:
  enabled: true
  otlp_endpoint: "http://10.0.0.200:6006/v1/traces"
  project_name: "llama-line"

alert_webhook_url: "https://ntfy.subcult.tv/nuc-alerts?title=llama-line%20alert&priority=4&tags=llama-line,warning"
alert_queue_full_threshold: 180
```
