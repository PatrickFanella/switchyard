# Create a Stack-Specific Template

Use this runbook when turning this base into a Go, Node/TypeScript, Python, Rust, React/Vite, or full-stack template.

## Steps

1. Create a new repository from `subculture-collective/project-template`.
2. Rename the project in `README.md`, `.env.example`, and docs.
3. Add the stack scaffold and dependency files.
4. Update `Makefile` so `make verify` runs the stack's real checks.
5. Update `AGENTS.md` with stack-specific guidance.
6. Run:

   ```bash
   make verify
   docker compose config --quiet
   ```

7. Bootstrap Open Pilot labels:

   ```bash
   open-pilot labels bootstrap OWNER/REPO
   ```

8. Create a small Open Pilot smoke issue with a deterministic `Test Command`.
9. Queue it only after confirming the issue body is complete.

## Verification

The derived template is ready when:

- `make verify` passes.
- The docs hub points to stack-specific docs.
- The Open Pilot issue template still requires `Test Command`.
- No secrets or generated dependency caches are committed.
