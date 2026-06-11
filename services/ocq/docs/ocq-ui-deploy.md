# ocq UI deploy

## Shape

- `ocq` gateway process stays separate from the UI.
- UI is a static Vite app/container under `apps/ui`.
- Gateway serves API + ops endpoints; UI talks to it over HTTP.

## Auth

- UI sits behind proxy auth.
- Gateway bearer key is still required for API calls.
- Do not treat proxy auth as a replacement for the gateway key.

## Deploy checklist

- Build UI separately from gateway.
- Run gateway on private network/host boundary.
- Put proxy auth in front of the UI.
- Keep API key handling client-side only for the logged-in operator.

## Metrics / NUC monitoring

- Gateway exposes Prometheus `GET /metrics`.
- Verify the active NUC monitoring path before wiring anything new.
- Do not assume the scraper path/name; confirm the current NUC collector / scrape target first.
- After confirming the active path, wire `/metrics` into that path only.
- Check that the scrape works and no secret labels/values appear.

## Safety checks

- Session picker is read-only.
- `send` is the only state-changing action.
- Request logs must not include prompt text by default.
- Metrics/logs/docs must not contain secrets.
- Keep any session, prompt, or auth data out of the UI cache except the MVP API key in `localStorage`.
