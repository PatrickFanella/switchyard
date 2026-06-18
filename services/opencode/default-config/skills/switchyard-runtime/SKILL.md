---
name: switchyard-runtime
description: Use when working inside Switchyard's managed OpenCode container, Open Pilot runs, native model broker, or workflow executor behavior.
---

# Switchyard Runtime

Switchyard owns workflow runs, steps, events, artifacts, model-broker compatibility, and native OpenCode execution. Prefer Switchyard-native contracts over historical OCQ, llama-line, or Changemaker service boundaries.

When making runtime changes:

1. Preserve API compatibility for `/api/chat`, `/api/generate`, `/v1/chat/completions`, `/api/models`, and Open Pilot routes.
2. Keep secrets out of logs and artifacts.
3. Run `make verify` before reporting completion.
4. Clean generated binaries with `git checkout -- backend/app && rm -f backend/switchyard` before committing.
