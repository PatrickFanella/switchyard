# llama-line Operator Runbook

Use this for installs, upgrades, and incident response. See `README.md` for full product and API details.

## 1. Initial Setup

1. Move `ollama` to port `11435`.

```ini
[Service]
Environment="OLLAMA_HOST=127.0.0.1:11435"
```

```bash
systemctl daemon-reload && systemctl restart ollama
curl http://127.0.0.1:11435/api/version
```

2. Install the `llama-line` binary.

```bash
make build && make install
```

```bash
cp bin/llama-line /usr/local/bin/llama-line
```

3. Create the config directory and file.

```bash
mkdir -p /etc/llama-line
```

```bash
cp config.example.yaml /etc/llama-line/config.yaml
```

4. Install the systemd unit.

```bash
cp deploy/llama-line.service /etc/systemd/system/
```

5. Enable and start the service.

```bash
systemctl daemon-reload && systemctl enable --now llama-line
```

6. Verify the broker.

```bash
curl http://localhost:11434/broker/status
```

## 2. Configuration Reference

See `README.md` for the full config table. Operational fields:

- `listen_addr` — bind address for the broker.
- `ollama_url` — must match the actual `ollama` address and port.
- `max_queue_depth` — tune for expected concurrency.
- `request_timeout_secs` — raise for large models or long prompts.
- `keys` — static API keys defined in config.

## 3. Key Management

1. Edit `/etc/llama-line/config.yaml`.
2. Add or remove keys under `keys`.
3. Restart the service.

```bash
systemctl restart llama-line
```

Keys are static in v1. No live reload.

## 4. Upgrading

1. Build a new binary.

```bash
make build
```

2. Install the new binary.

```bash
make install
```

```bash
cp bin/llama-line /usr/local/bin/llama-line
```

3. Restart the service.

```bash
systemctl restart llama-line
```

4. Watch logs.

```bash
journalctl -u llama-line -f
```

## 5. Viewing Logs

```bash
journalctl -u llama-line -f
```

```bash
journalctl -u llama-line --since "1 hour ago"
```

Log fields: `time`, `level`, `msg`, plus structured event fields.

Key messages:

- `llama-line starting` — startup with `listen_addr` and `ollama_url`.
- `listening` — server bound successfully.
- `client disconnected while queued` — queued client dropped.
- `server error` — fatal bind/listen error.
- `llama-line shutting down` — graceful shutdown started.

## 6. Health Check

```bash
curl http://localhost:11434/broker/status
```

Check `queue_depth`:

- Near `max_depth` consistently: raise `max_queue_depth` or investigate slow requests.

Check `connected_clients`:

- Active SSE connections waiting or streaming.

## 7. Troubleshooting

### Broker won't start: `address already in use`

1. Check what is bound to the port.

```bash
ss -tlnp | grep <port>
```

2. Common causes:

- `ollama` not moved to `11435`
- a `socat` bridge container or other listener on the port

### 401 on all requests

1. Confirm the request has an auth header.

```http
Authorization: Bearer <key>
```

2. Verify the key exists in `/etc/llama-line/config.yaml`.
3. Use `/broker/status` to confirm the broker is up; it does not require auth.

### 503 queue full

1. Increase `max_queue_depth`.
2. Restart the service.

```bash
systemctl restart llama-line
```

3. If it persists, check `request_timeout_secs` and request latency.

### Requests timing out

1. Increase `request_timeout_secs` for large model/context requests.
2. Verify upstream `ollama`.

```bash
curl http://127.0.0.1:11435/api/version
```

### `ollama_unavailable` SSE events

1. Confirm `ollama` is running.

```bash
systemctl status ollama
```

2. Confirm `ollama_url` matches the real upstream address.
3. The broker retries with backoff up to `ollama_retry_max_secs`.

### Clients not receiving SSE heartbeats

1. Check `heartbeat_interval_secs` in config.
2. Ensure no proxy or load balancer buffers SSE responses.
3. Disable buffering with `X-Accel-Buffering: no` or equivalent.

## 8. Stopping / Restarting

```bash
systemctl stop llama-line
```

- Stops with a graceful shutdown path and 30s drain window.
- In-flight requests are not drained in v1; clients will see an error.

```bash
systemctl restart llama-line
```

- Restarts immediately.
- In-flight requests will be cancelled.

## 9. Uninstall

1. Stop and disable the service.

```bash
systemctl disable --now llama-line
```

2. Remove the unit file.

```bash
rm /etc/systemd/system/llama-line.service
```

3. Reload systemd.

```bash
systemctl daemon-reload
```

4. Remove the binary.

```bash
rm /usr/local/bin/llama-line
```

5. Move `ollama` back to `11434` by removing `OLLAMA_HOST` from the drop-in, then restart `ollama`.
