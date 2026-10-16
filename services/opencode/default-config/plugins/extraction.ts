const AGENT = 'switchyard-news-extractor';

// This profile is selected by the broker only for the explicit extraction
// contract. Do not infer policy from instructions inside the source article.
export default {
  id: 'switchyard.extraction',
  setup: async (ctx) => {
    await ctx.session.hook('context', (event) => {
      if (event.agent !== AGENT) return;
      if (event.model.providerID !== 'openai' || event.model.id !== 'gpt-6.1-sol') {
        throw new Error('Extraction profile requires the selected OpenAI model');
      }
      event.options.maxTokens = 1024;
      event.options.reasoningEffort = 'low';
      event.tools = {};
    });
    await ctx.session.hook('retry', (event) => {
      if (event.agent === AGENT) event.decision = { retry: false };
    });
  },
};
