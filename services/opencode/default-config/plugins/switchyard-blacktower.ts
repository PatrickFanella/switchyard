import { persistCredential } from './managed-credentials.mjs';

const SESSION_CREATE_TIMEOUT_MS = Number(process.env.OPENCODE_SESSION_CREATE_TIMEOUT_MS ?? 5000);
const SESSION_MESSAGE_TIMEOUT_MS = Number(process.env.OPENCODE_MESSAGE_TIMEOUT_MS ?? 600000);

export default {
  id: 'switchyard.managed',
  setup: async (ctx) => {
    await ctx.session.hook('context', async (event) => {
      const { data: providers } = await ctx.provider.list({});
      const provider = providers.find((item) => item.id === event.model.providerID);
      if (provider?.integrationID) {
        const connection = await ctx.integration.connection.active(provider.integrationID);
        if (connection) {
          persistCredential(provider.integrationID, await ctx.integration.connection.resolve(connection));
        }
      }
      event.options.metadata = {
        ...(event.options.metadata ?? {}),
        switchyard_managed: true,
        session_create_timeout_ms: SESSION_CREATE_TIMEOUT_MS,
        session_message_timeout_ms: SESSION_MESSAGE_TIMEOUT_MS,
      };
    });
  },
};
