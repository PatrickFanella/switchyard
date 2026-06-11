import type {
  GiteaBranch,
  GiteaCommit,
  GiteaPullRequest,
  GiteaRepoSearchResult,
  GiteaRepository,
} from './types.js';

type FetchLike = typeof fetch;

export type GiteaClientOptions = {
  baseUrl: string;
  token?: string;
  fetchImpl?: FetchLike;
  pageLimit?: number;
};

export class GiteaClient {
  private readonly baseUrl: string;
  private readonly token?: string;
  private readonly fetchImpl: FetchLike;
  private readonly pageLimit: number;

  constructor(options: GiteaClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, '');
    this.token = options.token;
    this.fetchImpl = options.fetchImpl ?? fetch;
    this.pageLimit = options.pageLimit ?? 50;
  }

  async searchRepos(options: { owner?: string; repo?: string } = {}): Promise<GiteaRepository[]> {
    if (options.owner && options.repo) {
      return [await this.getRepo(options.owner, options.repo)];
    }

    const params = new URLSearchParams({
      limit: String(this.pageLimit),
      sort: 'updated',
      order: 'desc',
    });

    if (options.owner) {
      return this.paginateArray<GiteaRepository>(`/api/v1/users/${encodeURIComponent(options.owner)}/repos?limit=${this.pageLimit}`)
        .then((repos) => repos.filter((repo) => !repo.private));
    }

    const result = await this.paginate<GiteaRepoSearchResult>(`/api/v1/repos/search?${params}`);
    const repos = result.flatMap((page) => page.data).filter((repo) => !repo.private);
    return repos;
  }

  async getRepo(owner: string, repo: string): Promise<GiteaRepository> {
    return this.getJson<GiteaRepository>(`/api/v1/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`);
  }

  async listPullRequests(owner: string, repo: string): Promise<GiteaPullRequest[]> {
    return this.paginateArray<GiteaPullRequest>(
      `/api/v1/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/pulls?state=all&sort=recentupdate&limit=${this.pageLimit}`,
    );
  }

  async listBranches(owner: string, repo: string): Promise<GiteaBranch[]> {
    return this.paginateArray<GiteaBranch>(
      `/api/v1/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/branches?limit=${this.pageLimit}`,
    );
  }

  async listCommits(
    owner: string,
    repo: string,
    options: { branch: string; since: Date; until?: Date },
  ): Promise<GiteaCommit[]> {
    const params = new URLSearchParams({
      sha: options.branch,
      since: options.since.toISOString(),
      limit: String(this.pageLimit),
      files: 'false',
      verification: 'false',
    });
    if (options.until) {
      params.set('until', options.until.toISOString());
    }

    return this.paginateArray<GiteaCommit>(
      `/api/v1/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/commits?${params}`,
    );
  }

  async getPullRequestDiff(owner: string, repo: string, index: number): Promise<string> {
    return this.getText(`/api/v1/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/pulls/${index}.diff`);
  }

  async getCommitDiff(owner: string, repo: string, sha: string): Promise<string> {
    return this.getText(
      `/api/v1/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/git/commits/${encodeURIComponent(sha)}.diff`,
    );
  }

  private async paginateArray<T>(pathWithQuery: string): Promise<T[]> {
    const pages = await this.paginate<T[]>(pathWithQuery);
    return pages.flat();
  }

  private async paginate<T>(pathWithQuery: string): Promise<T[]> {
    const results: T[] = [];
    let page = 1;

    while (true) {
      const url = new URL(`${this.baseUrl}${pathWithQuery}`);
      url.searchParams.set('page', String(page));
      if (!url.searchParams.has('limit')) {
        url.searchParams.set('limit', String(this.pageLimit));
      }

      const response = await this.request(url);
      results.push((await response.json()) as T);

      if (!hasNextPage(response, page)) {
        break;
      }
      page += 1;
    }

    return results;
  }

  private async getJson<T>(path: string): Promise<T> {
    const response = await this.request(new URL(`${this.baseUrl}${path}`));
    return (await response.json()) as T;
  }

  private async getText(path: string): Promise<string> {
    const response = await this.request(new URL(`${this.baseUrl}${path}`));
    return response.text();
  }

  private async request(url: URL): Promise<Response> {
    const headers = new Headers({ Accept: 'application/json' });
    if (this.token) {
      headers.set('Authorization', `token ${this.token}`);
    }

    const response = await this.fetchImpl(url, { headers });
    if (!response.ok) {
      throw new Error(`Gitea request failed: ${response.status} ${response.statusText} ${url.pathname}`);
    }
    return response;
  }
}

function hasNextPage(response: Response, currentPage: number): boolean {
  const link = response.headers.get('link');
  if (link?.includes('rel="next"')) {
    return true;
  }

  const total = response.headers.get('x-total-count');
  if (!total) {
    return false;
  }

  const totalCount = Number.parseInt(total, 10);
  const limitText = new URL(response.url).searchParams.get('limit') ?? '50';
  const limit = Number.parseInt(limitText, 10);
  if (!Number.isFinite(totalCount) || !Number.isFinite(limit) || limit <= 0) {
    return false;
  }

  return currentPage * limit < totalCount;
}
