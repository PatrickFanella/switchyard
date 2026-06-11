import { z } from 'zod';

export const summarySchema = z.object({
  headline: z.string(),
  userFacingSummary: z.string(),
  technicalSummary: z.string(),
  impact: z.enum(['feature', 'fix', 'docs', 'infra', 'refactor', 'security', 'performance', 'other']),
  notableFiles: z.preprocess(normalizeNotableFiles, z.array(z.string())),
  confidence: z.coerce.number().min(0).max(1),
});

export function parseSummaryJson(content: string): z.infer<typeof summarySchema> {
  return summarySchema.parse(JSON.parse(extractJsonObject(content)));
}

function extractJsonObject(content: string): string {
  const trimmed = content.trim();
  if (trimmed.startsWith('{') && trimmed.endsWith('}')) return trimmed;

  const fenced = trimmed.match(/```(?:json)?\s*([\s\S]*?)```/i)?.[1]?.trim();
  if (fenced?.startsWith('{') && fenced.endsWith('}')) return fenced;

  const start = trimmed.indexOf('{');
  const end = trimmed.lastIndexOf('}');
  if (start >= 0 && end > start) return trimmed.slice(start, end + 1);

  throw new Error('LLM summary response did not contain a JSON object');
}

function normalizeNotableFiles(value: unknown): unknown {
  if (!Array.isArray(value)) return value;
  return value.map((item) => {
    if (typeof item === 'string') return item;
    if (item && typeof item === 'object') {
      const record = item as Record<string, unknown>;
      return String(record.path ?? record.file ?? record.name ?? JSON.stringify(record));
    }
    return String(item);
  });
}
