# Switchyard UI Feature Migration

Switchyard is absorbing the operator-facing UI ideas from the reference repos into one native HUD. The goal is not separate service consoles; it is native module surfaces backed by Switchyard APIs.

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
- `GET /api/adapters` still returns typed transitional contracts for imported/reference surfaces.
- Pages are wired so those contracts can evolve into native module state without changing the overall HUD layout.

## Next adapter work

1. Replace llama-line queue/request/upstream placeholders with native model broker state.
2. Replace OCQ requests/logs/sessions/settings placeholders with native OpenCode executor state.
3. Replace Blacktower placeholders with native profile/persona and workflow-role state.
4. Replace Changemaker placeholders with native content workflow policy, prompts, artifacts, and run state.
