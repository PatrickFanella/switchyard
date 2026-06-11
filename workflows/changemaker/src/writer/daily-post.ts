import { buildPostWriterContext } from './post-context.js';
import type { NarrativeState } from '../narrative/state.js';
import type { ChangeCluster, ChangesOutput } from '../types.js';
import type { PublicChangeEvidence } from './post-context.js';

export type DailyPost = {
  title: string;
  summary: string;
  markdown: string;
  tweetDrafts: string[];
  socialPosts: SocialPost[];
  coveredChangeIds: string[];
  narrativeNotes: DailyPostNarrativeNotes;
};

export type DailyPostOptions = {
  narrativeState?: NarrativeState;
  reportKind?: ReportKind;
  reportDateOffsetDays?: number;
  includeSocialSection?: boolean;
};

export type ReportKind = 'daily' | 'weekly' | 'monthly' | 'quarterly';

export type SocialPost = {
  channel: 'twitter';
  text: string;
  suggestedTime: string;
};

export type DailyPostNarrativeNotes = {
  whyItMatters: string;
  watchingNext: string;
};

export function writeDailyPost(output: ChangesOutput, options: DailyPostOptions = {}): DailyPost {
  const context = buildPostWriterContext(output, options.narrativeState);
  const reportKind = options.reportKind ?? 'daily';
  const reportDateOffsetDays = options.reportDateOffsetDays ?? 1;
  const includeSocialSection = options.includeSocialSection ?? true;
  const title = titleForOutput(output, reportKind, reportDateOffsetDays);
  const summary = summaryForOutput(output, context);
  const narrativeNotes = narrativeNotesForOutput(output, context);
  const socialPosts = socialPostsForOutput(output, context, summary, narrativeNotes, reportKind);
  const tweetDrafts = socialPosts.map((post) => post.text);
  const sections = bodySectionsForOutput(output, context, summary, narrativeNotes, socialPosts, includeSocialSection);
  const markdown = [`# ${title}`, '', ...sections].join('\n');

  return {
    title,
    summary,
    markdown: `${markdown.trim()}\n`,
    tweetDrafts,
    socialPosts,
    coveredChangeIds: output.changes.map((change) => change.id),
    narrativeNotes,
  };
}

function titleForOutput(output: ChangesOutput, reportKind: ReportKind, reportDateOffsetDays: number): string {
  const date = formatDate(addDays(new Date(output.window.to), -reportDateOffsetDays).toISOString());
  const lead = output.clusters[0]?.theme ?? 'workbench notes';
  const label = reportKind === 'daily' ? 'field report' : `${reportKind} field report`;
  return `${date} ${label}: ${sentenceCase(lead)}`;
}

function summaryForOutput(output: ChangesOutput, context: ReturnType<typeof buildPostWriterContext>): string {
  if (output.changes.length === 0) {
    return 'Quiet window: no uncovered public changes landed.';
  }

  const leadCluster = context.majorClusters[0] ?? context.backgroundClusters[0];
  if (!leadCluster) {
    return `${humanCount(output.changes.length, 'public change')} landed across ${humanCount(output.totals.repos, 'repo')}.`;
  }

  const leadEvidence = clusterEvidence(context, leadCluster)[0];
  const leadHeadline = leadEvidence?.publicSummary ?? leadEvidence?.title ?? stripThemePrefix(leadCluster.summary);
  const detail = firstDistinctText([
    leadEvidence?.technicalSummary,
    stripThemePrefix(leadCluster.summary),
    leadCluster.theme,
  ]);

  return joinSummaryParts(leadHeadline, detail);
}

function bodySectionsForOutput(
  output: ChangesOutput,
  context: ReturnType<typeof buildPostWriterContext>,
  summary: string,
  narrativeNotes: DailyPostNarrativeNotes,
  socialPosts: SocialPost[],
  includeSocialSection: boolean,
): string[] {
  if (output.changes.length === 0) {
    const sections = [
      '## Introduction',
      '',
      'Quiet window. Nothing public landed in this slice, so this stays short and factual instead of pretending there was momentum.',
      '',
      '## What changed',
      '',
      'No uncovered public changes landed.',
      '',
      '## Why it matters',
      '',
      narrativeNotes.whyItMatters,
      '',
      '## What to watch next',
      '',
      narrativeNotes.watchingNext,
      '',
      '## Conclusion',
      '',
      'Nothing to inflate here; wait for the next slice of real evidence.',
      '',
      '## Receipts',
      '',
      '- No uncovered changes',
    ];
    return includeSocialSection
      ? [...sections.slice(0, -3), ...socialSection(socialPosts), '', ...sections.slice(-3)]
      : sections;
  }

  const whatChanged = whatChangedSection(output, context);
  const conclusion = conclusionForOutput(context, summary);

  return [
    introductionForOutput(output, context),
    whatChanged,
    ['## Why it matters', '', narrativeNotes.whyItMatters].join('\n'),
    ['## What to watch next', '', narrativeNotes.watchingNext].join('\n'),
    ['## Conclusion', '', conclusion].join('\n'),
    ...(includeSocialSection ? [socialSection(socialPosts).join('\n')] : []),
    ['## Receipts', '', ...linkLines(context.changeEvidence, 12)].join('\n'),
  ];
}

function introductionForOutput(
  output: ChangesOutput,
  context: ReturnType<typeof buildPostWriterContext>,
): string {
  const lastPost = context.history.recentPosts[0];
  const thread = context.history.activeThreads[0];
  const futureWork = context.history.futureWork[0];
  const pieces = [
    lastPost ? `Yesterday's thread was: ${lastPost.summary}` : 'This update keeps the story grounded in public changes, not vibes.',
    thread ? `The main thread is ${thread.title.toLowerCase()}; ${stripTrailingPeriod(thread.summary)}` : '',
    futureWork ? `The queue still has ${futureWork.title.toLowerCase()} waiting, so the tone stays measured.` : '',
    output.changes.length > 0 ? `The broad shape is ${summaryForOutput(output, context)}.` : '',
  ].filter(Boolean);

  return ['## Introduction', '', ...paragraphs(pieces)].join('\n');
}

function whatChangedSection(output: ChangesOutput, context: ReturnType<typeof buildPostWriterContext>): string {
  if (output.changes.length === 0) {
    return ['## What changed', '', 'No uncovered public changes landed.'].join('\n');
  }

  const clusters = context.majorClusters.length ? context.majorClusters : context.backgroundClusters.slice(0, 2);
  const lines = clusters.map((cluster) => {
    const evidence = clusterEvidence(context, cluster);
    const lead = evidence[0];
    const examples = evidence.slice(0, 2).map((item) => item.linkText).join(' and ');
    const clusterHeadline = joinSummaryParts(
      lead?.publicSummary ?? lead?.title ?? stripThemePrefix(cluster.summary),
      firstDistinctText([lead?.technicalSummary, stripThemePrefix(cluster.summary)]),
    );
    const evidenceDetails = evidence
      .slice(1, 2)
      .map((item) => evidenceSentence(item))
      .join(' ');
    return `- **${sentenceCase(cluster.theme)}.** ${clusterHeadline}${evidenceDetails ? ` ${evidenceDetails}` : ''}${examples ? ` (${examples})` : ''}`;
  });

  const background = context.backgroundClusters
    .filter((cluster) => !clusters.includes(cluster))
    .slice(0, 3)
    .map((cluster) => {
      const lead = clusterEvidence(context, cluster)[0];
      return `- ${sentenceCase(cluster.theme)} stays in the background${lead ? `: ${lead.publicSummary}` : ''}.`;
    });

  return ['## What changed', '', ...lines, ...(background.length > 0 ? ['', ...background] : [])].join('\n');
}

function narrativeNotesForOutput(output: ChangesOutput, context: ReturnType<typeof buildPostWriterContext>): DailyPostNarrativeNotes {
  return {
    whyItMatters: whyItMattersNote(output, context),
    watchingNext: watchingNextNote(output, context),
  };
}

function whyItMattersNote(output: ChangesOutput, context: ReturnType<typeof buildPostWriterContext>): string {
  if (output.changes.length === 0) {
    return 'Quiet window; do not inflate public narrative. Preserve prior threads until new evidence appears.';
  }
  const leadEvidence = context.changeEvidence[0];
  const futureWork = context.history.futureWork[0];
  const leadFacts = leadEvidence ? joinSummaryParts(leadEvidence.publicSummary, leadEvidence.technicalSummary) : `${humanCount(output.totals.changes, 'change')} landed`;
  const followUp = futureWork ? ` ${futureWork.title} is still queued, so the next slice should be judged against that open work.` : '';
  return `This matters because the update points to concrete changes, not broad claims: ${leadFacts}.${followUp}`;
}

function watchingNextNote(output: ChangesOutput, context: ReturnType<typeof buildPostWriterContext>): string {
  const open = output.changes.filter((change) => change.type === 'pull_request' && change.state === 'open').length;
  const futureWork = context.history.futureWork[0];
  const openLine = open > 0 ? ` ${humanCount(open, 'open pull request')} is still open.` : '';
  const futureLine = futureWork ? ` The next queued item is ${futureWork.title.toLowerCase()}.` : '';
  return `Next pass, watch whether those open items close out or stay open.${openLine}${futureLine} The useful signal is the status of the follow-up work.`;
}

function conclusionForOutput(context: ReturnType<typeof buildPostWriterContext>, summary: string): string {
  const thread = context.history.activeThreads[0];
  const futureWork = context.history.futureWork[0];
  const parts = [
    `The short version: ${summary}.`,
    thread ? `The thread to keep watching is ${thread.title.toLowerCase()}.` : '',
    futureWork ? `There is still ${futureWork.title.toLowerCase()} on deck.` : '',
    'The next update should show whether this became sturdier or simply quieter.',
  ].filter(Boolean);
  return parts.join(' ');
}

function socialPostsForOutput(
  output: ChangesOutput,
  context: ReturnType<typeof buildPostWriterContext>,
  summary: string,
  narrativeNotes: DailyPostNarrativeNotes,
  reportKind: ReportKind,
): SocialPost[] {
  if (output.changes.length === 0) {
    return [];
  }

  const leadCluster = context.majorClusters[0] ?? context.backgroundClusters[0];
  const leadEvidence = leadCluster ? clusterEvidence(context, leadCluster)[0] : undefined;
  const leadHeadline = leadEvidence?.publicSummary ?? leadEvidence?.title ?? 'Today';
  const thread = context.history.recentPosts[0]?.summary;
  const futureWork = context.history.futureWork[0]?.title;

  const label = reportKind === 'daily' ? 'Field report' : `${sentenceCase(reportKind)} field report`;
  return normalizeAndTrimTweets([
    leadEvidence ? `${label}: ${leadHeadline}. ${firstDistinctText([leadEvidence.technicalSummary, summary])}.` : `${label}: ${summary}.`,
    leadEvidence
      ? `Receipt: ${leadEvidence.linkText} — ${firstDistinctText([leadEvidence.publicSummary, leadEvidence.technicalSummary])}.`
      : `Receipt: ${summary}.`,
    `Yesterday's thread was ${thread ?? 'quiet'}. ${stripTrailingPeriod(narrativeNotes.whyItMatters)}.`,
    futureWork ? `Next up: ${futureWork}. ${stripTrailingPeriod(narrativeNotes.watchingNext)}.` : stripTrailingPeriod(narrativeNotes.watchingNext),
  ]).map((text, index) => ({ channel: 'twitter', text, suggestedTime: suggestedPostingTimes[index] ?? '16:30' }));
}

const suggestedPostingTimes = ['09:00', '12:30', '15:30', '18:00'];

function normalizeAndTrimTweets(drafts: string[]): string[] {
  const seen = new Set<string>();
  const tweets: string[] = [];

  for (const draft of drafts) {
    const normalized = draft.replace(/\s+/g, ' ').trim();
    if (!normalized || seen.has(normalized)) continue;
    const clipped = normalized.length > 280 ? `${normalized.slice(0, 277).trimEnd()}...` : normalized;
    if (seen.has(clipped)) continue;
    seen.add(clipped);
    tweets.push(clipped);
  }

  return tweets;
}

function socialSection(posts: SocialPost[]): string[] {
  if (posts.length === 0) {
    return ['## Twitter / X posts', '', '_No social posts for a quiet window._'];
  }

  return [
    '## Twitter / X posts',
    '',
    ...posts.flatMap((post, index) => [
      `### Post ${index + 1} — ${post.suggestedTime}`,
      '',
      `> ${post.text}`,
      '',
    ]),
  ].filter((line, index, lines) => !(line === '' && index === lines.length - 1));
}

function paragraphs(lines: string[]): string[] {
  return lines.flatMap((line, index) => (index === 0 ? [line] : ['', line]));
}

function linkLines(evidence: PublicChangeEvidence[], limit: number): string[] {
  if (evidence.length === 0) return ['- No uncovered changes'];
  const shown = evidence.slice(0, limit).map((item) => `- ${item.linkText} — ${evidenceSentence(item)}`);
  const hidden = evidence.length - shown.length;
  return hidden > 0 ? [...shown, `- …and ${hidden} more changes in the JSON artifact.`] : shown;
}

function clusterEvidence(context: ReturnType<typeof buildPostWriterContext>, cluster: ChangeCluster): PublicChangeEvidence[] {
  const byId = new Map(context.changeEvidence.map((item) => [item.id, item]));
  return cluster.eventIds.map((eventId) => byId.get(eventId)).filter((item): item is PublicChangeEvidence => Boolean(item));
}

function evidenceSentence(item: PublicChangeEvidence): string {
  return joinSummaryParts(item.publicSummary, item.technicalSummary);
}

function joinSummaryParts(first: string, second?: string): string {
  const parts = [first, second].filter((part): part is string => Boolean(part && part.trim()));
  const distinct: string[] = [];
  for (const part of parts) {
    if (distinct.some((existing) => normalizeSummary(existing) === normalizeSummary(part))) continue;
    distinct.push(part);
  }
  if (distinct.length === 0) return '';
  if (distinct.length === 1) return distinct[0];
  return `${distinct[0]} — ${distinct[1]}`;
}

function firstDistinctText(values: Array<string | undefined>): string | undefined {
  const unique: string[] = [];
  for (const value of values) {
    if (!value || !value.trim()) continue;
    if (unique.some((existing) => normalizeSummary(existing) === normalizeSummary(value))) continue;
    unique.push(value);
  }
  return unique[0];
}

function normalizeSummary(value: string): string {
  return value.replace(/\s+/g, ' ').trim().toLowerCase().replace(/[.\s]+$/g, '');
}

function stripThemePrefix(value: string): string {
  return value.replace(/^[^:]+:\s*/, '');
}

function stripTrailingPeriod(value: string): string {
  return value.replace(/[.\s]+$/, '');
}

function sentenceCase(value: string): string {
  return value.length > 0 ? `${value[0]?.toUpperCase()}${value.slice(1)}` : value;
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat('en', { month: 'short', day: 'numeric' }).format(new Date(value));
}

function addDays(date: Date, days: number): Date {
  return new Date(date.getTime() + days * 24 * 60 * 60 * 1000);
}

function humanCount(count: number, singular: string): string {
  return `${count} ${count === 1 ? singular : `${singular}s`}`;
}
