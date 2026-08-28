# Open Pilot repository contracts

Phase 1 defines two non-secret JSON contracts without enabling multi-repository execution:

- [Operator registry schema](open-pilot-operator-registry-v1.schema.json) describes operator-owned host inputs.
- [Runtime manifest schema](open-pilot-runtime-manifest-v1.schema.json) describes the generated, read-only runtime view.

Both contracts use version `1`, permit 1–32 repositories, reject undeclared fields, and require stable lowercase ASCII IDs and generations of at most 32 characters. Repository slugs preserve the registry's exact ASCII `owner/repo` spelling. Runtime lookup case-folds the canonical slug and every explicit alias, but returns the exact registered slug.

IDs, case-folded slugs and aliases, and executor generations are globally unique in one document. Aliases cannot shadow another canonical slug. Files contain references to password files only; they must not contain credentials, bearer values, tokens, passwords, or embedded secret material. Endpoints cannot contain user information, queries, or fragments.

Issue numbers are not manifest fields. Runtime target parsing accepts ASCII decimal digits only, requires a value greater than zero that fits in a signed 64-bit integer, and emits the canonical decimal representation. Thus `01` becomes `1`; whitespace, signs, zero, and overflow fail.

The runtime fields for source paths, managed worktree roots, service endpoints, and password-file references reserve the Phase 2 shape. Their presence in Phase 1 does not authorize routing to a second repository.
