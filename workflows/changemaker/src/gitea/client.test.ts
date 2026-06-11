import { describe, expect, it } from 'vitest';
import { GiteaClient } from './client.js';

describe('GiteaClient', () => {
  it('paginates repository search results', async () => {
    const requestedUrls: string[] = [];
    const fetchImpl = async (input: URL | RequestInfo) => {
      const url = String(input);
      requestedUrls.push(url);
      const page = new URL(url).searchParams.get('page');
      const body = {
        ok: true,
        data:
          page === '1'
            ? [repo('subculture-collective/one')]
            : [repo('subculture-collective/two')],
      };
      return jsonResponse(body, url, { 'x-total-count': '2' });
    };

    const client = new GiteaClient({
      baseUrl: 'https://git.subcult.tv',
      fetchImpl: fetchImpl as typeof fetch,
      pageLimit: 1,
    });

    const repos = await client.searchRepos();

    expect(repos.map((item) => item.full_name)).toEqual([
      'subculture-collective/one',
      'subculture-collective/two',
    ]);
    expect(requestedUrls).toHaveLength(2);
  });

  it('uses the user repos endpoint when an owner is provided', async () => {
    const requestedUrls: string[] = [];
    const fetchImpl = async (input: URL | RequestInfo) => {
      const url = String(input);
      requestedUrls.push(url);
      return jsonResponse([repo('PatrickFanella/ocq'), { ...repo('PatrickFanella/private'), private: true }], url);
    };

    const client = new GiteaClient({
      baseUrl: 'https://git.subcult.tv',
      fetchImpl: fetchImpl as typeof fetch,
    });

    const repos = await client.searchRepos({ owner: 'PatrickFanella' });

    expect(requestedUrls[0]).toContain('/api/v1/users/PatrickFanella/repos');
    expect(repos.map((item) => item.full_name)).toEqual(['PatrickFanella/ocq']);
  });
});

function repo(fullName: string) {
  const [, name] = fullName.split('/');
  return {
    name,
    full_name: fullName,
    private: false,
    html_url: `https://git.subcult.tv/${fullName}`,
    default_branch: 'main',
    owner: { login: 'subculture-collective' },
  };
}

function jsonResponse(body: unknown, url: string, headers: Record<string, string> = {}): Response {
  const response = new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json', ...headers },
  });
  Object.defineProperty(response, 'url', { value: url });
  return response;
}
