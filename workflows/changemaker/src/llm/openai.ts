import OpenAI from 'openai';
import type { ChangeEvent, ChangeSummary } from '../types.js';
import type { LlmProvider } from './provider.js';
import { parseSummaryJson } from './summary-schema.js';

export type OpenAIProviderOptions = {
  apiKey: string;
  model: string;
};

export class OpenAIProvider implements LlmProvider {
  readonly name = 'openai';
  readonly model: string;
  private readonly client: OpenAI;

  constructor(options: OpenAIProviderOptions) {
    this.model = options.model;
    this.client = new OpenAI({ apiKey: options.apiKey });
  }

  async summarizeChange(change: ChangeEvent): Promise<ChangeSummary> {
    const response = await this.client.responses.create({
      model: this.model,
      input: [
        {
          role: 'system',
          content:
            'Summarize Gitea code changes for a technical audience following product progress. Return strict JSON only.',
        },
        {
          role: 'user',
          content: JSON.stringify({
            instructions: {
              fields: ['headline', 'userFacingSummary', 'technicalSummary', 'impact', 'notableFiles', 'confidence'],
              impactValues: ['feature', 'fix', 'docs', 'infra', 'refactor', 'security', 'performance', 'other'],
              confidenceRange: '0..1',
            },
            change,
          }),
        },
      ],
    });

    return parseSummaryJson(response.output_text);
  }
}

export function createOpenAIProvider(options: { apiKey?: string; model: string }): OpenAIProvider {
  if (!options.apiKey) {
    throw new Error('OPENAI_API_KEY is required when --summarize uses the openai provider');
  }
  return new OpenAIProvider({ apiKey: options.apiKey, model: options.model });
}
