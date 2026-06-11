# Template Maintenance

Use this guide when changing the base template or propagating improvements to derived templates.

## Maintenance principles

- Keep this base stack-neutral.
- Put stack-specific scaffolds in child template repositories.
- Prefer reusable docs, Makefile targets, and Open Pilot conventions over app code.
- Avoid adding tools that require every derived project to inherit the same stack.

## Safe changes for this base

Good base-level changes include:

- issue and PR template improvements
- docs structure improvements
- generic security and contribution guidance
- Makefile targets that skip absent stacks cleanly
- `.gitignore`, `.gitattributes`, and editor defaults
- Open Pilot workflow documentation

Avoid base-level changes such as:

- adding `package.json`, `go.mod`, `pyproject.toml`, or `Cargo.toml`
- adding React/Vite/Tailwind scaffold
- adding production deployment assumptions
- requiring one package manager or runtime globally

## Update checklist

1. Create an issue describing why the base template should change.
2. Make the change in a branch.
3. Run:

   ```bash
   make verify
   docker compose config --quiet
   ```

4. Update docs if the workflow changes.
5. Open a PR and link the issue.
6. After merge, decide whether derived templates need the same change.

## Propagating to derived templates

For each derived template:

1. Review the base change for relevance.
2. Apply only the useful parts.
3. Preserve stack-specific verification commands.
4. Run the derived template's canonical verification command.
5. Note any intentional divergence in the derived template docs.

## Versioning convention

Use git tags when a base template update is meaningful enough for derived templates to target:

```bash
git tag vYYYY.MM.DD
git push origin vYYYY.MM.DD
```

Do not tag trivial typo fixes unless a derived template needs a stable reference.

## Open Pilot smoke test

After substantial template maintenance, create a small Open Pilot task that changes a docs-only file and uses a narrow test command such as:

```bash
test -f docs/reference/example.md
```

Queue it only after labels are bootstrapped and the issue body is complete.
