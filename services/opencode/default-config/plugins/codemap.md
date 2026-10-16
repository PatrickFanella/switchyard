# services/opencode/default-config/plugins/

## Responsibility
Adds Switchyard-specific OpenCode runtime metadata and RTK command rewriting.

## Design
- `switchyard-blacktower.ts` applies managed timeouts, tool-output limits, and request metadata through plugin hooks.
- `extraction.ts` bounds the dedicated OpenAI news-extraction agent, removes tools and vetoes retries.
- `rtk.ts` rewrites eligible shell commands to their RTK equivalents while preserving execution semantics.

## Flow
OpenCode loads configured plugin files; configuration hooks set defaults and chat/tool hooks annotate or optimize each managed session.

## Integration
Registered by `opencode.defaults.json` and copied into the active config by `install.sh`.
