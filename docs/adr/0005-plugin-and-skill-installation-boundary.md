# ADR 0005: Keep plugin and skill installation outside Switchyard

## Status

Accepted (defer) — 2026-09-24. Resolves Gitea issue #62. The owner named
below may revisit it when the revisit trigger is met.

## Context

Issue #6 lists a "broad plugin/skill installer" as an explicit non-goal, and
the roadmap excludes broad skill installation, OpenCode plugin management, an
agent marketplace, and arbitrary plugin hooks from native Blacktower. Issue #62
asks for that deferral to be explicit: which integration boundaries exist
today, what they do not solve, and what an installer would have to answer
before anyone proposes one.

This record was written against `main` at
`9a531064b9c93d110ce262345df9e79f67273980`.

### Supported integration boundaries

Every extension point below is either source-controlled in this repository and
built into an image, or operator data that Switchyard validates and audits.
None of them installs code at runtime.

| Boundary | What exists | Where it is enforced |
| --- | --- | --- |
| OpenCode runtime | `opencode-ai` 1.17.18, installed by `npm ci --omit=dev` from `services/opencode/package-lock.json`; `autoupdate: false`; `share: disabled` | `services/opencode/package.json`, `Dockerfile`, `default-config/opencode.defaults.json` |
| Image base and tools | `node:24-alpine` pinned by digest; Alpine packages pinned by version; RTK pinned by commit with a SHA-256 check of its installer | `services/opencode/Dockerfile` |
| OpenCode plugins | Two repository files, `rtk.ts` and `switchyard-blacktower.ts`, registered by absolute path | `default-config/plugins/`, `opencode.defaults.json` |
| OpenCode agents and skills | Twelve Markdown agent files and one skill (`switchyard-runtime`) copied from the image | `default-config/agents/`, `default-config/skills/`, `install.sh` |
| MCP servers | None. The managed defaults define no `mcp` block; strict mode never reads persisted configuration, so a persisted `mcp` entry cannot survive | `opencode.defaults.json`, `merge-opencode-config.mjs` |
| Strict bootstrap | `OPENCODE_SECURITY_MODE=strict` (the Compose default) deletes persisted `plugin(s)`, `agent(s)`, `command(s)`, `mode(s)`, `skill(s)`, `blacktower`, and `instruction(s)` trees, then writes the managed `opencode.json` whole. It rejects `OPENCODE_CONFIG`, `OPENCODE_CONFIG_CONTENT`, `OPENCODE_PERMISSION`, `OPENCODE_AUTH_CONTENT`, project config discovery, a global `CLAUDE.md`, and a non-memory database | `install.sh`; tests in `scripts/test_opencode_config.py` |
| OpenCode permissions | `bash`, `task`, `external_directory`, and `question` denied; `webfetch` and `websearch` set to `ask`; file tools allowed inside the managed worktree | `opencode.defaults.json` |
| Container isolation | Read-only root filesystem, all capabilities dropped except the three needed to initialize volumes, `no-new-privileges`, non-root runtime identity | `docker-compose.yml` `opencode` service |
| Model provider | One managed `switchyard` provider whose base URL must be the internal listener `http://api:11434/v1` | `merge-opencode-config.mjs` |
| Switchyard executors | Fixed set: `internal`, `model`, `opencode`, `command`, `http`, `git`, `gitea`. `command` uses argv allowlists; `http` requires `SWITCHYARD_HTTP_ALLOWED_HOSTS` | `backend/internal/controlplane`, `AGENTS.md` |
| Agent profiles | Operator-editable prompt contracts, allowed executors, and allowed models as immutable, audited versions with admin-only activation | `docs/reference/agent-profiles.md` |
| Workflow definitions | Operator-defined linear pipelines over the fixed executors, validated on write | `docs/reference/step-outputs.md` |

`OPENCODE_SECURITY_MODE=development` merges persisted plugins, MCP servers, and
permissions over the defaults. It is an explicit local mode. Production
configuration validation requires strict mode (see `docs/status.md`).

The supported way to add an OpenCode plugin, agent, or skill is a reviewed pull
request that adds the file under `services/opencode/default-config/`, followed
by an image rebuild and a normal release. Revoking one is the same path in
reverse.

### Operator problems these boundaries do not solve

- Adding a third-party OpenCode plugin, MCP server, or skill to production
  requires a source change, image rebuild, and redeploy. There is no runtime
  catalog and no per-repository or per-run tool selection.
- Revocation is a redeploy. There is no way to disable one extension on a
  running sidecar or to record which extension set served a given run.
- Operators cannot add a new Switchyard executor type or step hook without a
  code change.
- Extensions tried in development mode do not carry into production; the
  operator must port them into `default-config/` by hand.
- The managed provider entry names the npm package `@ai-sdk/openai-compatible`,
  which is not in `services/opencode/package-lock.json`. This record does not
  verify how OpenCode resolves that package at runtime; it is an existing
  dependency whose version this repository does not pin.

### Risks an installer would have to address

- **Provenance.** An installer needs a trusted source list, author identity,
  and a signature or reviewed-commit binding. Today provenance is the
  repository history and the image revision label.
- **Version pinning.** Tags and npm ranges drift. An installer would need
  content hashes for the extension and its transitive dependencies. The
  unpinned provider package above shows the gap already exists at small scale.
- **Permissions.** OpenCode plugins run in-process with the sidecar's
  filesystem and network access and can rewrite configuration through the
  `config` hook; `switchyard-blacktower.ts` already uses that hook to set
  timeouts and output limits. A third-party plugin could use the same hook to
  re-enable `bash` or `task`, so the permission table above would no longer be
  a guarantee. MCP servers are separate processes with their own network
  access and would need their own isolation.
- **Installation.** The sidecar root filesystem is read-only and strict
  bootstrap purges the config tree on every start. Runtime installation would
  need a new writable, integrity-checked location, which weakens both
  properties.
- **Revocation.** Removing an extension would require stopping in-flight
  sessions that loaded it, recording the extension set in the immutable run
  snapshot, and proving the removal took effect.
- **Supply chain.** npm install scripts, typosquatting, maintainer compromise,
  and silent updates all become runtime inputs to an executor that edits
  repositories and whose output Open Pilot can push and merge.

## Decision

Defer. Switchyard does not add a plugin or skill installer, a marketplace, or
arbitrary plugin hooks. The boundaries in the table above are the supported
integration surface. New OpenCode plugins, agents, and skills enter through a
reviewed change to `services/opencode/default-config/` and an image rebuild.

- **Owner:** the repository owner, the same role that holds external
  monitoring and CI-runner responsibility under issue #25.
- **Defer while:** the managed-in-repository path can deliver each requested
  extension within the owner's normal release cadence, and no workflow needs a
  per-repository or per-run tool set.
- **Revisit trigger:** a recorded issue that names a concrete extension and
  shows why a reviewed image change cannot meet the need, for example a
  required change cadence faster than releases or a per-repository tool set
  that one managed image cannot serve.
- **Proceed criteria:** the owner accepts a proposal that covers every risk
  above and does not weaken strict mode, repository admission, immutable-run
  identity, executor isolation, command allowlists, listener boundaries, or
  CI and merge gates.

Any approved expansion gets its own threat model and a bounded implementation
backlog of separately scoped issues before any code lands. This record
authorizes neither.

## Consequences

- Production OpenCode behavior stays determined by the image and the strict
  bootstrap; `scripts/test_opencode_config.py` remains the regression guard.
- Operators who want a new extension pay the cost of a pull request and a
  release.
- The unpinned provider package is recorded here as a known gap. Pinning it is
  separate work and is not done by this record.
