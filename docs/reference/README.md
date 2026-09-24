# Reference

Use this section for durable technical references:

- API contracts
- Environment variables
- Data models
- CLI commands
- Integration notes

Keep exploratory notes in `docs/research/` until they become stable references.

- [Client Compatibility](client-compatibility.md) — llama-line/Ollama/OpenAI compatibility behavior for downstream clients.
- [Agent Profiles](agent-profiles.md) — versioned profile identity, draft/active lifecycle, validation, audit, and run/step policy snapshots.
- [Evidence Search](evidence-search.md) — bounded, redacted control-plane search over retained runs, steps, and model requests.
- [Executor Sessions](executor-sessions.md) — bounded native OpenCode session view from run-step correlations and executor status.
- [Step outputs](step-outputs.md) — durable named step outputs, `{{steps.<step_id>.outputs.<name>}}` bindings, limits, output contracts (`output_shape`), and retry/resume restoration.
- [Devlog repository evidence](devlog-evidence.md) — allowlisted repository and time-window inputs, bounded Gitea collection, merged/deployed/proposed classification, and the `devlog-evidence.json` provenance artifact.
- [Runtime configuration](runtime-configuration.md) — settings inventory, secret redaction, and the v1 boundary between file-managed and runtime-mutable settings.
- [Workflow catalog](workflow-catalog.md) — dashboard catalog and linear draft editor for workflow definitions, validation messages, read-only access, and the draft-versus-active save semantics.
