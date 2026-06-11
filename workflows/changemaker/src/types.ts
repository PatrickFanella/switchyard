export type ChangeType = 'pull_request' | 'commit';

export type ChangeState = 'open' | 'closed' | 'merged';

export type RepoRef = {
  owner: string;
  name: string;
  fullName: string;
  defaultBranch: string;
  private: boolean;
  htmlUrl: string;
};

export type ChangeEvent = {
  id: string;
  type: ChangeType;
  repo: string;
  branch?: string;
  title: string;
  body?: string;
  author?: string;
  url: string;
  createdAt: string;
  updatedAt: string;
  mergedAt?: string;
  state?: ChangeState;
  sha?: string;
  prIndex?: number;
  diff?: string;
  diffFiles?: string[];
  diffTruncated?: boolean;
  summary?: ChangeSummary;
  covered: boolean;
};

export type ChangeSummary = {
  headline: string;
  userFacingSummary: string;
  technicalSummary: string;
  impact: 'feature' | 'fix' | 'docs' | 'infra' | 'refactor' | 'security' | 'performance' | 'other';
  notableFiles: string[];
  confidence: number;
};

export type ChangesOutput = {
  generatedAt: string;
  window: {
    from: string;
    to: string;
    lookbackHours: number;
  };
  source: {
    baseUrl: string;
    owner?: string;
    repo?: string;
  };
  totals: {
    repos: number;
    pullRequests: number;
    commits: number;
    changes: number;
  };
  clusters: ChangeCluster[];
  changes: ChangeEvent[];
};

export type ChangeCluster = {
  theme: string;
  eventIds: string[];
  summary: string;
  narrativeImportance: 'major' | 'minor' | 'background';
};
