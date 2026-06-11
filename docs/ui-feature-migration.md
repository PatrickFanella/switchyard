# Switchyard UI Feature Migration

Switchyard is absorbing the operator-facing UI surfaces from the four reference repos without collapsing their runtime boundaries.

## Implemented Switchyard homes

| Reference source | Existing UI surface | Switchyard home |
| --- | --- | --- |
| Blacktower control center | tasks, runs, reports, scheduler health, session commands | Overview run center, Agents, Skills |
| llama-line web/admin | queues, inflight requests, upstream health, stats, config, request/log/search | Queues, Logs/Search, Config |
| OCQ UI/admin | overview, requests, logs, sessions, settings | Sessions, Logs/Search, Config |
| Changemaker web | run control, live logs, posts, prompts, config, service status | Prompts, Config, Overview run center |

## Current state

- The web app uses the neubrutal HUD token system from `web/tailwind.config.js` and CSS variables emitted into `web/src/styles.css`.
- The dashboard has module navigation for Overview, Queues, Sessions, Logs/Search, Prompts, Config, Agents, and Skills.
- `GET /api/adapters` returns typed placeholder contracts for all imported UI surfaces.
- Pages are wired to those contracts so real adapters can replace static data without changing page layout.

## Next adapter work

1. Replace llama-line queue/request/upstream placeholders with calls to llama-line admin/status APIs.
2. Replace OCQ requests/logs/sessions/settings placeholders with OCQ admin APIs.
3. Replace Blacktower tasks/reports/scheduler placeholders with control-center snapshots or API calls.
4. Replace Changemaker posts/prompts/config/service placeholders with its admin API.
