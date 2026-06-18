const SESSION_CREATE_TIMEOUT_MS = Number(process.env.OPENCODE_SESSION_CREATE_TIMEOUT_MS ?? 5000);
const SESSION_MESSAGE_TIMEOUT_MS = Number(process.env.OPENCODE_MESSAGE_TIMEOUT_MS ?? 300000);

export default async function switchyardBlacktowerPlugin() {
  return {
    config: (cfg) => {
      cfg.experimental = { ...(cfg.experimental ?? {}), mcp_timeout: cfg.experimental?.mcp_timeout ?? 30000 };
      cfg.tool_output = { ...(cfg.tool_output ?? {}), max_lines: cfg.tool_output?.max_lines ?? 2000, max_bytes: cfg.tool_output?.max_bytes ?? 51200 };
    },
    'chat.params': async (_input, output) => {
      output.params = {
        ...(output.params ?? {}),
        metadata: {
          ...(output.params?.metadata ?? {}),
          switchyard_managed: true,
          session_create_timeout_ms: SESSION_CREATE_TIMEOUT_MS,
          session_message_timeout_ms: SESSION_MESSAGE_TIMEOUT_MS,
        },
      };
    },
  };
}
