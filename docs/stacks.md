# Stack Recipes

This base template intentionally avoids application scaffold. Use these recipes when creating stack-specific templates.

## Go

Add:

- `go.mod`
- `cmd/<app>/main.go`
- `internal/` packages
- `go test ./...` as the default test command

Recommended `make verify` coverage:

```bash
gofmt
go test ./...
golangci-lint run
```

## Node / TypeScript

Add:

- `package.json`
- `pnpm-lock.yaml`
- `tsconfig.json`
- `src/` or app-specific workspace directories

Recommended scripts:

```json
{
  "scripts": {
    "build": "tsc -b",
    "lint": "eslint .",
    "test": "vitest run",
    "format": "prettier --write ."
  }
}
```

## React / Vite / Tailwind

Add a frontend workspace such as `web/` and keep frontend commands explicit:

```bash
pnpm --dir web run build
pnpm --dir web run test
pnpm --dir web run lint
```

## Python

Add:

- `pyproject.toml`
- `src/<package>/`
- `tests/`

Recommended tools:

```bash
uv run pytest
uv run ruff check .
uv run ruff format .
```

## Rust

Add:

- `Cargo.toml`
- `src/`
- integration tests when useful

Recommended commands:

```bash
cargo fmt --all
cargo clippy --all-targets -- -D warnings
cargo test --all
```

## Postgres

This base includes an optional local Postgres compose profile:

```bash
docker compose --profile db up -d postgres
```

Derived templates should add migrations under `migrations/` or the stack-specific convention.

## Docker

Keep Docker defaults boring:

- Build repeatably from the repo root.
- Keep secrets in `.env` or secret managers, never in images.
- Make `docker compose config --quiet` pass in CI.
- Prefer profiles for optional local infrastructure.
