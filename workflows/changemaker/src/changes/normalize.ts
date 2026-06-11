import type { GiteaCommit, GiteaPullRequest, GiteaRepository } from '../gitea/types.js';
import type { ChangeEvent, ChangeState, RepoRef } from '../types.js';

export function toRepoRef(repo: GiteaRepository): RepoRef {
  const [owner = repo.owner.login ?? repo.owner.username ?? '', name = repo.name] = repo.full_name.split('/');
  return {
    owner,
    name,
    fullName: repo.full_name,
    defaultBranch: repo.default_branch,
    private: repo.private,
    htmlUrl: repo.html_url,
  };
}

export function pullRequestToChange(repo: RepoRef, pr: GiteaPullRequest): ChangeEvent {
  const state: ChangeState = pr.merged || pr.merged_at ? 'merged' : pr.state;
  return {
    id: `pr:${repo.fullName}#${pr.index}`,
    type: 'pull_request',
    repo: repo.fullName,
    branch: pr.head?.ref,
    title: pr.title,
    body: pr.body,
    author: displayUser(pr.user),
    url: pr.html_url,
    createdAt: pr.created_at,
    updatedAt: pr.updated_at,
    mergedAt: pr.merged_at ?? undefined,
    state,
    prIndex: pr.index,
    covered: false,
  };
}

export function commitToChange(repo: RepoRef, branch: string, commit: GiteaCommit): ChangeEvent {
  const title = firstLine(commit.commit.message);
  const committedAt = commit.created ?? commit.commit.committer?.date ?? commit.commit.author?.date ?? new Date(0).toISOString();

  return {
    id: `commit:${repo.fullName}@${commit.sha}`,
    type: 'commit',
    repo: repo.fullName,
    branch,
    title,
    body: commit.commit.message,
    author: displayUser(commit.author) ?? commit.commit.author?.name ?? commit.commit.committer?.name,
    url: commit.html_url,
    createdAt: committedAt,
    updatedAt: committedAt,
    sha: commit.sha,
    covered: false,
  };
}

export function sortChanges(changes: ChangeEvent[]): ChangeEvent[] {
  return [...changes].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
}

function firstLine(value: string): string {
  return value.split('\n')[0]?.trim() || 'Untitled commit';
}

function displayUser(user: { login?: string; username?: string; full_name?: string } | undefined): string | undefined {
  return user?.full_name || user?.login || user?.username;
}
