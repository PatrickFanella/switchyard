# ADR 0003: Rebuild Imported Apps as Native Switchyard Modules

## Status

Accepted — 2026-06-14

## Context

Switchyard imported useful source from OCQ, llama-line, and Changemaker, and kept Blacktower as reference material. ADR 0002 made that import explicit and preserved service boundaries so the repository could become runnable without a large rewrite. Those imported directories were later removed after the native module cutover.

That boundary was a migration tactic, not the product goal. Switchyard should not become a dashboard that tapes together several separate applications. The product direction is one cohesive automation runtime that absorbs the ideas and problem-solving from those projects into native Switchyard modules.

## Decision

Switchyard is the product and runtime. Imported service directories were reference implementations and transitional scaffolding until their useful concepts were rebuilt behind native Switchyard module contracts.

The end-state module map is:

```txt
Switchyard
  runtime
    runs, steps, triggers, leases, retries, cancellation
    events, artifacts, logs, operator summaries

  model broker module
    model routing, queueing, upstream health, request IDs
    inspired by llama-line, owned by Switchyard

  opencode executor module
    OpenCode sessions, request/session correlation, aborts
    inspired by OCQ, owned by Switchyard

  profile module
    curated agent/persona roles, prompt contracts, model defaults
    inspired by Blacktower, owned by Switchyard

  workflow modules
    Open Pilot, Devlog, Changemaker-style content policy
    domain policy only; generic scheduling/runtime stays in Switchyard

  HUD/API
    one operator surface for all modules
```

## Consequences

- Root Docker Compose may start compatibility sidecars or adapters while native module parity is built.
- New workflow code should call Switchyard-native executor/model interfaces, not service-specific APIs directly.
- The web UI should present native Switchyard module state, not separate service consoles.
- Changemaker and Devlog should become workflow policy modules, not independently scheduled apps.
- Blacktower means native profiles/personas in Switchyard, not an imported app or marketplace.
- Switchyard exposes llama-line/Ollama-compatible ingress on `LLAMA_LINE_PORT` only for cutover: `/broker/status`, `/v1/chat/completions`, `/api/chat`, `/api/generate`, `/api/tags`, `/v1/models`, `/api/embed`, `/v1/embeddings`, and admin queue/inflight endpoints. The native broker routes provider-style models to OpenCode and can route Ollama/local models to a separate upstream `OLLAMA_BASE_URL`.

## Migration Plan

1. Keep the current stack runnable while marking imported services as transitional.
2. Define stable native module contracts: model broker, OpenCode executor, workflow module, agent profile, module health, cancellation handle.
3. Move all workflows to those native contracts.
4. Absorb OCQ behavior into a native OpenCode executor.
5. Absorb llama-line behavior into a native model broker module.
6. Convert Changemaker behavior into Switchyard workflow policy and artifacts.
7. Replace adapter placeholders with native module health/status/logs.
8. Remove or archive transitional sidecars once native parity is proven.

## Relationship to ADR 0002

ADR 0002 remains the historical import decision. This ADR supersedes its long-term boundary guidance: copied source is no longer treated as owning permanent runtime domains. Switchyard owns the cohesive runtime; imported apps are historical references only.
