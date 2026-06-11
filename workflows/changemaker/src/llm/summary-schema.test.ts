import { describe, expect, it } from 'vitest';
import { parseSummaryJson } from './summary-schema.js';

describe('parseSummaryJson', () => {
  it('extracts fenced summary JSON and normalizes loose fields', () => {
    const summary = parseSummaryJson(`So far:\n\n\`\`\`json\n{
      "headline": "Done",
      "userFacingSummary": "User",
      "technicalSummary": "Tech",
      "impact": "feature",
      "notableFiles": [{"path":"src/app.ts"}],
      "confidence": "0.7"
    }\n\`\`\``);

    expect(summary.notableFiles).toEqual(['src/app.ts']);
    expect(summary.confidence).toBe(0.7);
  });
});
