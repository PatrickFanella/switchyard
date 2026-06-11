import { commitToChange, pullRequestToChange, sortChanges, toRepoRef } from '../changes/normalize.js';
import { clusterChanges } from '../changes/cluster.js';
import { filterDiff } from '../changes/diff-filter.js';
import type { ChangeEvent, ChangesOutput, RepoRef } from '../types.js';
import type { GiteaClient } from './client.js';

export type CrawlOptions = {
  baseUrl: string;
  owner?: string;
  repo?: string;
  since: Date;
  now: Date;
  lookbackHours: number;
  coveredChangeIds?: Set<string>;
  includeCovered?: boolean;
  includeDiffs?: boolean;
  maxDiffChars?: number;
};

export async function crawlChanges(client: GiteaClient, options: CrawlOptions): Promise<ChangesOutput> {
  const sourceRepos = await client.searchRepos({ owner: options.owner, repo: options.repo });
  const repos = sourceRepos.filter((repo) => !repo.updated_at || new Date(repo.updated_at) >= options.since).map(toRepoRef);
  const changes: ChangeEvent[] = [];

  for (const repo of repos) {
    changes.push(...(await collectRepoChanges(client, repo, options.since, options.now)));
  }

  const withCoverage = dedupeChanges(changes).map((change) => ({
    ...change,
    covered: options.coveredChangeIds?.has(change.id) ?? false,
  }));
  const sortedChanges = sortChanges(
    options.includeCovered ? withCoverage : withCoverage.filter((change) => !change.covered),
  );
  const enrichedChanges = options.includeDiffs !== false
    ? await attachDiffs(client, sortedChanges, options.maxDiffChars ?? 12_000)
    : sortedChanges;
  const pullRequests = enrichedChanges.filter((change) => change.type === 'pull_request').length;
  const commits = enrichedChanges.filter((change) => change.type === 'commit').length;

  return {
    generatedAt: options.now.toISOString(),
    window: {
      from: options.since.toISOString(),
      to: options.now.toISOString(),
      lookbackHours: options.lookbackHours,
    },
    source: {
      baseUrl: options.baseUrl,
      owner: options.owner,
      repo: options.repo,
    },
    totals: {
      repos: repos.length,
      pullRequests,
      commits,
      changes: enrichedChanges.length,
    },
    clusters: clusterChanges(enrichedChanges),
    changes: enrichedChanges,
  };
}

async function attachDiffs(client: GiteaClient, changes: ChangeEvent[], maxDiffChars: number): Promise<ChangeEvent[]> {
  const enriched: ChangeEvent[] = [];
  for (const change of changes) {
    const [owner, repo] = change.repo.split('/');
    if (!owner || !repo) {
      enriched.push(change);
      continue;
    }

    const rawDiff = await fetchChangeDiff(client, owner, repo, change);
    if (!rawDiff) {
      enriched.push(change);
      continue;
    }

    const filtered = filterDiff(rawDiff, maxDiffChars);
    enriched.push({
      ...change,
      diff: filtered.text,
      diffFiles: filtered.files,
      diffTruncated: filtered.truncated,
    });
  }
  return enriched;
}

async function fetchChangeDiff(
  client: GiteaClient,
  owner: string,
  repo: string,
  change: ChangeEvent,
): Promise<string | undefined> {
  try {
    if (change.type === 'pull_request' && change.prIndex !== undefined) {
      return await client.getPullRequestDiff(owner, repo, change.prIndex);
    }
    if (change.type === 'commit' && change.sha) {
      return await client.getCommitDiff(owner, repo, change.sha);
    }
  } catch {
    return undefined;
  }
  return undefined;
}

async function collectRepoChanges(client: GiteaClient, repo: RepoRef, since: Date, until: Date): Promise<ChangeEvent[]> {
  const changes: ChangeEvent[] = [];

  const prs = await client.listPullRequests(repo.owner, repo.name);
  changes.push(
    ...prs
      .filter((pr) => isWithinWindow(new Date(pr.updated_at), since, until))
      .map((pr) => pullRequestToChange(repo, pr)),
  );

  const branches = await safeListBranches(client, repo);
  const seenCommitShas = new Set<string>();
  for (const branch of branches) {
    const commits = await client.listCommits(repo.owner, repo.name, { branch, since, until });
    for (const commit of commits) {
      const committedAt = new Date(commit.created ?? commit.commit.committer?.date ?? commit.commit.author?.date ?? 0);
      if (!isWithinWindow(committedAt, since, until)) {
        continue;
      }
      if (seenCommitShas.has(commit.sha)) {
        continue;
      }
      seenCommitShas.add(commit.sha);
      changes.push(commitToChange(repo, branch, commit));
    }
  }

  return changes;
}

function isWithinWindow(date: Date, since: Date, until: Date): boolean {
  const time = date.getTime();
  return Number.isFinite(time) && time >= since.getTime() && time <= until.getTime();
}

async function safeListBranches(client: GiteaClient, repo: RepoRef): Promise<string[]> {
  try {
    const branches = await client.listBranches(repo.owner, repo.name);
    const names = branches.map((branch) => branch.name).filter(Boolean);
    return names.length > 0 ? names : [repo.defaultBranch];
  } catch {
    return [repo.defaultBranch];
  }
}

function dedupeChanges(changes: ChangeEvent[]): ChangeEvent[] {
  const seen = new Set<string>();
  return changes.filter((change) => {
    if (seen.has(change.id)) {
      return false;
    }
    seen.add(change.id);
    return true;
  });
}
