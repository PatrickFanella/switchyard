# services/opencode/default-config/

## Responsibility
Defines Switchyard-managed OpenCode defaults: agent profiles, plugins, skills, references, permissions, compaction, and output limits.

## Design
- Markdown agent/profile contracts represent Blacktower roles without a separate application boundary.
- Strict mode replaces mounted configuration with `opencode.defaults.json`; development mode de-duplicates arrays and applies user values over defaults.
- Strict bootstrap removes persisted plugin, agent, command, mode, and skill trees before copying managed files.
- `sanitize-opencode-auth.mjs` atomically retains only recognized API/OAuth provider credentials in strict persistent auth state; malformed or unsafe auth files fail startup closed.

## Flow
`install.sh` sanitizes strict auth state, purges unmanaged extensions, copies agents/plugins/skills, then replaces or development-merges `opencode.json` before OpenCode starts.

## Integration
- Packaged by `services/opencode/Dockerfile`.
- Runtime skill and agent contracts guide Open Pilot and delegated work inside the managed container.
