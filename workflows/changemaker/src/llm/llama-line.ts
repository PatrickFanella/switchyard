import type { ChangeEvent, ChangeSummary } from '../types.js';
import type { LlmProvider } from './provider.js';
import { parseSummaryJson } from './summary-schema.js';

export type LlamaLineProviderOptions = {
  baseUrl: string;
  apiKey: string;
  model: string;
  fetchImpl?: typeof fetch;
};

export class LlamaLineProvider implements LlmProvider {
  readonly name = 'llama-line';
  readonly model: string;
  private readonly baseUrl: string;
  private readonly apiKey: string;
  private readonly fetchImpl: typeof fetch;

  constructor(options: LlamaLineProviderOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, '');
    this.apiKey = options.apiKey;
    this.model = options.model;
    this.fetchImpl = options.fetchImpl ?? fetch;
  }

  async summarizeChange(change: ChangeEvent): Promise<ChangeSummary> {
    const response = await this.fetchImpl(`${this.baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${this.apiKey}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        model: this.model,
        stream: false,
        messages: [
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
      }),
    });

    if (!response.ok) {
      throw new Error(`llama-line request failed: ${response.status} ${response.statusText}`);
    }

    const completion = parseLlamaLineResponse(await response.text());
    const content = completion.choices?.[0]?.message?.content;
    if (!content) {
      throw new Error('llama-line response did not include assistant content');
    }
    return parseSummaryJson(content);
  }
}

export function createLlamaLineProvider(options: {
  baseUrl: string;
  apiKey?: string;
  model: string;
}): LlamaLineProvider {
  if (!options.apiKey) {
    throw new Error('LLAMA_LINE_API_KEY is required when --llm-provider llama-line is used');
  }
  return new LlamaLineProvider({ baseUrl: options.baseUrl, apiKey: options.apiKey, model: options.model });
}

export function parseLlamaLineSse(text: string): { choices?: Array<{ message?: { content?: string } }> } {
  for (const line of text.split('\n')) {
    if (!line.startsWith('data: ')) {
      continue;
    }
    const payloadText = line.slice('data: '.length).trim();
    if (!payloadText || payloadText === '[DONE]') {
      continue;
    }
    const payload = JSON.parse(payloadText) as {
      status?: string;
      message?: string;
      choices?: Array<{ message?: { content?: string } }>;
    };
    if (payload.status === 'queued') {
      continue;
    }
    if (payload.status === 'ollama_unavailable' || payload.status === 'dropped_by_admin') {
      throw new Error(`llama-line terminal status: ${payload.status}${payload.message ? `: ${payload.message}` : ''}`);
    }
    return payload;
  }
  throw new Error('llama-line SSE stream did not include a completion payload');
}

export function parseLlamaLineResponse(text: string): { choices?: Array<{ message?: { content?: string } }> } {
  const trimmed = text.trim();
  if (!trimmed.startsWith('data: ')) {
    return JSON.parse(trimmed) as { choices?: Array<{ message?: { content?: string } }> };
  }
  return parseLlamaLineSse(text);
}
