# llama-line Runtime

## Start dependencies

```bash
docker compose --profile db up -d postgres
```

## Install/update systemd service

```bash
sudo ./deploy/install-systemd.sh
```

## Status

```bash
systemctl status llama-line --no-pager
journalctl -u llama-line -n 100 --no-pager
curl -fsS http://127.0.0.1:11434/broker/status | python3 -m json.tool
```

## Web UI

- Local: `http://127.0.0.1:11434/ui/`
- LAN: `http://10.0.0.50:11434/ui/`
- Public: `https://llama-line.subcult.tv/ui/`

The public route is hosted by almaz Caddy and protected by Authelia:

```caddy
http://llama-line.subcult.tv {
    import authelia
    reverse_proxy 10.0.0.50:11434
}
```

## Manual fallback

```bash
go build -o bin/llama-line ./cmd/app
setsid "$PWD/bin/llama-line" --config config.yaml > tmp/llama-line.out 2>&1 < /dev/null &
```
