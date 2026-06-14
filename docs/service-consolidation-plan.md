---
title: Service Consolidation Plan
created: 2026-06-11
tags:
  - architecture
  - opencode
  - orchestration
  - ai-agents
---

# Service Consolidation Plan

> **Superseded direction:** This document is retained as historical design input. The current architecture decision is [ADR 0003: Rebuild Imported Apps as Native Switchyard Modules](adr/0003-native-switchyard-modules.md). Switchyard should not keep Blacktower/OCQ/llama-line/Changemaker as separate product boundaries; it should absorb their useful ideas into native Switchyard modules.

## Core Recommendation

Keep **blacktower** standalone.

Merge **llama-line** and **ocq** into one orchestration runtime.

Treat **open-pilot** as a first-party workflow or app on top of that runtime.

Keep **changemaker** standalone for now, but design it so it can later become a scheduled workflow inside the larger runtime.

```txt
blacktower
  standalone installer / suite / operator

llama-line + ocq
  merged orchestration runtime

open-pilot
  first-party repo automation workflow

changemaker
  standalone now, scheduled workflow later
```

---

# Project Roles

## blacktower

**Role:** Installer, suite manager, and local operator.

blacktower should remain separate because it prepares and manages the environment rather than acting as the runtime itself.

It can handle:

- installing opencode
- installing plugins
- installing skills
- configuring local orchestration
- starting/stopping the runtime
- running diagnostics
- managing local developer setup

blacktower should be able to install or manage the merged runtime, but it should not contain the core orchestration logic.

### Boundary

```txt
blacktower installs and operates the system.
blacktower does not run the job system itself.
```

---

## llama-line

**Role:** Model router, queue, and orchestration layer.

llama-line is core infrastructure. It decides what jobs need to run, where they should go, and how model/tool work should be routed.

It should own:

- model routing
- queueing
- job state
- pipeline dispatch
- worker coordination
- logs and events
- model/provider configuration

---

## ocq

**Role:** opencode execution layer.

ocq should be merged into llama-line and become the **opencode executor** inside the larger runtime.

Instead of being a separate service, it should become something like:

```txt
executors/
  opencode/
  shell/
  http/
```

### Why merge llama-line and ocq?

Keeping them separate creates unnecessary complexity:

- duplicated config
- awkward job ownership
- extra network calls
- harder tracing
- harder debugging
- split responsibility over execution state

Together, they form one natural control loop:

```txt
request
  -> queue
  -> route
  -> spawn opencode session
  -> capture logs/artifacts
  -> return result
  -> trigger downstream action
```

---

## open-pilot

**Role:** Git issue automation workflow.

open-pilot should probably not be the core runtime, but it should live very close to it.

It is a productized workflow that uses the runtime to:

- scan Gitea for issues
- filter by label or assignee
- create jobs
- run opencode pipelines
- commit changes
- open PRs
- merge completed work

### Recommended shape

Make open-pilot a **first-party module** or **app** built on top of the merged llama-line/ocq runtime.

```txt
apps/
  open-pilot/
    issue-scanner
    label-rules
    assignment-rules
    pipeline-definitions
    merge-policy
    pr-comments
```

### Boundary

```txt
The runtime knows how to run jobs.
open-pilot knows how to turn Git issues into jobs.
```

---

## changemaker

**Role:** Scheduled devlog/blog generator.

changemaker can remain standalone for now. It has a clean product identity and does not need to be merged immediately.

It currently does something like:

```txt
every 24 hours
  -> collect recent changes
  -> call llama-line
  -> generate devlog/blog post
  -> publish or save result
```

Over time, it could become part of a more general scheduled workflow system.

### Future shape

```yaml
workflows:
  changemaker:
    schedule: "daily"
    lookback: "24h"
    pipeline: "generate_devlog"
```

### Boundary

```txt
changemaker is not core infrastructure.
changemaker is a scheduled workflow that uses core infrastructure.
```

---

# Recommended Architecture

## Two-Repo Model

This is probably the cleanest structure.

```txt
blacktower/
  installer
  skill management
  plugin management
  opencode setup
  runtime management

line/
  core runtime
  queue
  model router
  opencode executor
  pipeline engine
  scheduler
  worker system
  dashboard
  first-party workflows
```

The merged runtime could be named something like:

- `line`
- `line-runtime`
- `llama-line`
- `blackline`
- `ocline`
- `towerline`

The exact name matters less than the boundary.

---

# Internal Runtime Structure

The merged runtime should be organized around stable infrastructure concepts.

```txt
runtime/
  api/
  queue/
  jobs/
  pipelines/
  models/
  executors/
    opencode/
    shell/
    http/
  scheduler/
  artifacts/
  events/
  secrets/
  git/
  workflows/
    open-pilot/
    changemaker/
```

## Core Runtime Concepts

The runtime should understand:

- jobs
- pipelines
- steps
- executors
- model requests
- queues
- workers
- schedules
- events
- artifacts
- logs
- secrets
- repositories

The runtime should **not** directly center concepts like:

- blog posts
- devlogs
- issue fixing
- PR merging
- opencode-specific business logic

Those should live in workflows or executors.

---

# Product Boundary

## blacktower answers:

> How do I install, configure, and operate this opencode ecosystem?

## llama-line/ocq runtime answers:

> How do I run reliable AI/dev/tool jobs?

## open-pilot answers:

> How do I automatically complete Git issues?

## changemaker answers:

> How do I turn recent project activity into devlogs or status posts?

---

# Suggested Consolidation Path

## Phase 1: Merge llama-line and ocq

Create one runtime that owns:

- queue
- model routing
- opencode execution
- job state
- logs
- artifacts
- worker lifecycle

Treat ocq as the `opencode executor`.

```txt
llama-line
  + ocq
  = orchestration runtime
```

---

## Phase 2: Move open-pilot onto the runtime

Refactor open-pilot so it creates runtime jobs instead of managing execution itself.

open-pilot should own Git-specific behavior:

- issue scanning
- label filtering
- assignment logic
- pipeline selection
- branch/PR/merge policy

The runtime should own execution.

---

## Phase 3: Add scheduler support

Add a general scheduler to the runtime.

This should support:

- daily jobs
- interval jobs
- cron-like schedules
- repository polling
- recurring reports
- maintenance tasks

---

## Phase 4: Decide whether changemaker folds in

Once scheduler support exists, changemaker can either remain standalone or become a workflow module.

Recommended default:

```txt
Keep changemaker standalone until the runtime scheduler is stable.
Then fold it in as a scheduled workflow if it reduces duplication.
```

---

# Avoiding the Monolith Problem

The risk is not having fewer services.

The risk is merging unrelated concepts.

It is fine to have one repo or one deployable runtime, as long as the internal boundaries stay clean.

```txt
core runtime != Git automation
core runtime != blog generation
core runtime != installer
core runtime != opencode itself
```

A good runtime should be generic enough that both open-pilot and changemaker feel like natural users of it.

---

# Final Shape

```txt
blacktower
  standalone installer / suite / local operator

line-runtime
  merged llama-line + ocq
  queue
  router
  executors
  scheduler
  pipelines
  logs
  artifacts

open-pilot
  first-party workflow for Git issue automation

changemaker
  standalone app now
  possible scheduled workflow later
```

## Final Recommendation

Move toward **one orchestration runtime** plus **separate workflow modules**.

Do not keep five small services if they are all reinventing queueing, config, logging, model routing, and execution state.

But also do not collapse everything into one conceptually tangled app.

The best architecture is:

```txt
blacktower = installer/operator

llama-line + ocq = runtime

open-pilot + changemaker = apps/workflows on top
```

---

# Code-Informed Update: 2026-06-11

After inspecting the actual `blacktower`, `llama-line`, `ocq`, and `changemaker` implementations, the consolidation direction remains useful, but the immediate move should be **contract hardening rather than repo/service merger**.

## Current implementation reality

- `llama-line` is a synchronous HTTP broker for model/inference requests. It has queues, routing, upstream health probes, auth, observability, and optional persistence, but it is not a durable job runtime.
- `ocq` is a small OpenAI-compatible gateway over an OpenCode server. It is useful as an upstream, but it is not yet a full executor package with artifact capture, durable lifecycle state, or live log streaming.
- `changemaker` already works well as a standalone scheduled application that can call `llama-line` as an LLM backend.
- `blacktower` is an OpenCode plugin/operator suite, not only an installer, and should remain separate.

## Decision after hardening

Do **not** fold `ocq` into `llama-line` as an internal executor package yet.

Keep this shape for now:

```txt
blacktower
  OpenCode plugin / installer / operator UI

llama-line
  shared auth, queueing, model routing, observability, and upstream gateway

ocq
  private OpenCode gateway behind llama-line

changemaker
  standalone scheduled app using llama-line
```

This keeps the services separate while making the `llama-line -> ocq` boundary explicit and testable.

## Hardened contract

The current `llama-line -> ocq` contract should be:

- OCQ health probes support both `/health` and `/healthz`.
- `llama-line` strips client `Authorization` and sends configured upstream bearer auth to OCQ.
- `llama-line` sends `X-Llama-Line-Request-ID` upstream for correlation.
- OCQ accepts `X-Request-ID` or `X-Llama-Line-Request-ID` and returns `x-ocq-request-id`.
- OCQ accepts `ocq_session_id` for both streaming and non-streaming chat-completion requests.
- OCQ returns `x-ocq-session`; `llama-line` copies it back and injects `ocq_session_id` into non-streaming JSON bodies when available.
- OCQ exposes `x-ocq-status` and, for streaming compatibility responses, `x-ocq-streaming-mode: buffered`.
- OCQ aborts the upstream OpenCode request when the downstream client disconnects before completion.

## Next merge gate

Revisit making OCQ an internal executor only after at least one of these is true:

- `llama-line` grows a durable job table and lifecycle state machine.
- Open Pilot needs artifact/log capture that cannot be expressed through the current OpenAI-compatible OCQ gateway.
- Multiple workflows need a shared executor interface with cancellation, workdir isolation, artifacts, and structured outputs.
- Live OpenCode event streaming becomes a required product behavior.

Until then, the lowest-risk architecture is separate services with a stronger protocol boundary.
