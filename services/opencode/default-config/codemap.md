# services/opencode/default-config/

## Responsibility
Defines Switchyard-managed OpenCode defaults: agent profiles, plugins, skills, references, permissions, compaction, and output limits.

## Design
- Markdown agent/profile contracts represent Blacktower roles without a separate application boundary.
- `opencode.defaults.json` is merged rather than replacing mounted user configuration.
- `merge-opencode-config.mjs` de-duplicates arrays and applies user values over defaults.

## Flow
`install.sh` copies agents/plugins/skills, then the merge script combines defaults with existing `opencode.json` before OpenCode starts.

## Integration
- Packaged by `services/opencode/Dockerfile`.
- Runtime skill and agent contracts guide Open Pilot and delegated work inside the managed container.
