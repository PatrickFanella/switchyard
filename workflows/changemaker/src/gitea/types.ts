export type GiteaUser = {
  login?: string;
  username?: string;
  full_name?: string;
};

export type GiteaRepository = {
  name: string;
  full_name: string;
  private: boolean;
  html_url: string;
  default_branch: string;
  updated_at?: string;
  owner: GiteaUser;
};

export type GiteaRepoSearchResult = {
  ok: boolean;
  data: GiteaRepository[];
};

export type GiteaPullRequest = {
  id: number;
  index: number;
  title: string;
  body?: string;
  state: 'open' | 'closed';
  html_url: string;
  created_at: string;
  updated_at: string;
  merged_at?: string | null;
  merged?: boolean;
  user?: GiteaUser;
  head?: {
    ref?: string;
    sha?: string;
  };
};

export type GiteaBranch = {
  name: string;
};

export type GiteaCommit = {
  sha: string;
  html_url: string;
  created?: string;
  author?: GiteaUser;
  commit: {
    message: string;
    author?: {
      name?: string;
      date?: string;
    };
    committer?: {
      name?: string;
      date?: string;
    };
  };
};
