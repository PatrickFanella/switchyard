import { describe, expect, it } from 'vitest';
import { LlamaLineProvider, parseLlamaLineResponse, parseLlamaLineSse } from './llama-line.js';

describe('parseLlamaLineSse', () => {
  it('skips queue events and returns completion payload', () => {
    const completion = parseLlamaLineSse(
      [
        'data: {"request_id":"abc","position":1,"status":"queued"}',
        '',
        'data: {"choices":[{"message":{"content":"{\\"headline\\":\\"Done\\"}"}}]}',
        '',
      ].join('\n'),
    );

    expect(completion.choices?.[0]?.message?.content).toBe('{"headline":"Done"}');
  });

  it('throws on terminal broker errors', () => {
    expect(() => parseLlamaLineSse('data: {"status":"dropped_by_admin"}\n')).toThrow('dropped_by_admin');
  });

  it('accepts plain OpenAI-compatible JSON responses', () => {
    const completion = parseLlamaLineResponse(
      '{"choices":[{"message":{"content":"{\\"headline\\":\\"Done\\"}"}}]}',
    );

    expect(completion.choices?.[0]?.message?.content).toBe('{"headline":"Done"}');
  });

  it('normalizes object notableFiles from model JSON', async () => {
    const provider = new LlamaLineProvider({
      baseUrl: 'http://fixture',
      apiKey: 'key',
      model: 'model',
      fetchImpl: async () =>
        new Response(
          JSON.stringify({
            choices: [
              {
                message: {
                  content: JSON.stringify({
                    headline: 'Done',
                    userFacingSummary: 'User summary',
                    technicalSummary: 'Technical summary',
                    impact: 'feature',
                    notableFiles: [{ path: 'src/app.ts' }],
                    confidence: '0.8',
                  }),
                },
              },
            ],
          }),
          { status: 200 },
        ),
    });

    const summary = await provider.summarizeChange({
      id: 'commit:repo@sha',
      type: 'commit',
      repo: 'owner/repo',
      title: 'Change',
      url: 'https://example.test',
      createdAt: '2026-05-25T00:00:00.000Z',
      updatedAt: '2026-05-25T00:00:00.000Z',
      covered: false,
    });

    expect(summary.notableFiles).toEqual(['src/app.ts']);
    expect(summary.confidence).toBe(0.8);
  });
});
