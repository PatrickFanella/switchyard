import type { ChangeCluster, ChangeEvent } from '../types.js';

export function clusterChanges(changes: ChangeEvent[]): ChangeCluster[] {
  const groups = new Map<string, ChangeEvent[]>();
  for (const change of changes) {
    const theme = themeForChange(change);
    groups.set(theme, [...(groups.get(theme) ?? []), change]);
  }

  return [...groups.entries()]
    .map(([theme, events]) => ({
      theme,
      eventIds: events.map((event) => event.id),
      summary: summarizeCluster(theme, events),
      narrativeImportance: importanceForEvents(events),
    }))
    .sort((a, b) => importanceRank(a.narrativeImportance) - importanceRank(b.narrativeImportance));
}

function themeForChange(change: ChangeEvent): string {
  if (change.summary) {
    return themeFromImpact(change.summary.impact);
  }

  const text = `${change.title} ${change.body ?? ''}`.toLowerCase();
  if (/(fix|bug|crash|error|broken|repair)/.test(text)) return 'Fixes and reliability';
  if (/(doc|readme|guide|comment)/.test(text)) return 'Documentation and communication';
  if (/(test|spec|coverage|vitest)/.test(text)) return 'Testing and verification';
  if (/(docker|ci|deploy|infra|config|build)/.test(text)) return 'Infrastructure and tooling';
  if (/(refactor|cleanup|simplif)/.test(text)) return 'Code cleanup and refactors';
  return change.type === 'pull_request' ? 'Product progress' : 'Code progress';
}

function themeFromImpact(impact: NonNullable<ChangeEvent['summary']>['impact']): string {
  switch (impact) {
    case 'feature':
      return 'Product progress';
    case 'fix':
      return 'Fixes and reliability';
    case 'docs':
      return 'Documentation and communication';
    case 'infra':
      return 'Infrastructure and tooling';
    case 'refactor':
      return 'Code cleanup and refactors';
    case 'security':
      return 'Security hardening';
    case 'performance':
      return 'Performance work';
    default:
      return 'Code progress';
  }
}

function summarizeCluster(theme: string, events: ChangeEvent[]): string {
  const headlines = events
    .slice(0, 3)
    .map((event) => event.summary?.headline ?? event.title)
    .join('; ');
  return `${theme}: ${headlines}`;
}

function importanceForEvents(events: ChangeEvent[]): ChangeCluster['narrativeImportance'] {
  if (events.some((event) => event.type === 'pull_request') || events.length >= 3) {
    return 'major';
  }
  if (events.length === 2) {
    return 'minor';
  }
  return 'background';
}

function importanceRank(importance: ChangeCluster['narrativeImportance']): number {
  return { major: 0, minor: 1, background: 2 }[importance];
}
