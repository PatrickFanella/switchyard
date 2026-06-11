# Agent Instructions

This repository is Changemaker, a TypeScript CLI/service for turning public Gitea pull requests and commits into daily narrative Markdown updates.

## Priorities

1. Keep the CLI runnable and minimal.
2. Preserve Open Pilot issue and PR templates.
3. Keep generated posts, caches, secrets, local SQLite databases, and raw diffs out of git unless explicitly intended as fixtures.
4. Update this file, `README.md`, and `docs/implementation-plan.md` when product or stack conventions change.
5. Prefer Gitea API access over local git clones for V1 collection.

## Verification

Run:

```bash
make verify
```

Use narrower commands only when an issue explicitly asks for a smaller check.
