export type RunRequest = {
  kind: 'daily' | 'manual';
  since?: string;
  until?: string;
};

export type RunStatus = {
  state: 'idle' | 'running' | 'cancelled' | 'succeeded' | 'failed';
  startedAt?: string;
  finishedAt?: string;
  error?: string;
  request?: RunRequest;
  logs: string[];
};

export type RunControllerRunner = (request: RunRequest, log: (line: string) => void) => Promise<void>;

export type RunController = {
  getStatus(): RunStatus;
  start(request: RunRequest): Promise<void>;
  cancel(): boolean;
};

const MAX_LOG_LINES = 200;

export function createRunController(deps: { run?: RunControllerRunner } = {}): RunController {
  let status: RunStatus = { state: 'idle', logs: [] };
  let active: { epoch: number } | undefined;
  let epoch = 0;
  const run = deps.run ?? defaultRun;

  return {
    getStatus: () => cloneStatus(status),
    start: async (request: RunRequest): Promise<void> => {
      if (active) throw new Error('A run is already in progress');

      const runEpoch = ++epoch;

      status = {
        state: 'running',
        startedAt: new Date().toISOString(),
        request: cloneRequest(request),
        logs: ['started'],
      };

      const log = (line: string) => {
        if (active?.epoch !== runEpoch) return;

        status = {
          ...status,
          logs: [...status.logs.slice(-(MAX_LOG_LINES - 1)), line].slice(-MAX_LOG_LINES),
        };
      };

      const execution = Promise.resolve()
        .then(() => run(request, log))
        .then(() => {
          if (active?.epoch !== runEpoch) return;

          status = {
            ...status,
            state: 'succeeded',
            finishedAt: new Date().toISOString(),
            error: undefined,
            logs: [...status.logs, 'finished'].slice(-MAX_LOG_LINES),
          };
        })
        .catch((error: unknown) => {
          if (active?.epoch !== runEpoch) return;

          const message = error instanceof Error ? error.message : String(error);
          status = {
            ...status,
            state: 'failed',
            finishedAt: new Date().toISOString(),
            error: message,
            logs: [...status.logs, `failed: ${message}`].slice(-MAX_LOG_LINES),
          };
          throw error;
        })
        .finally(() => {
          if (active?.epoch === runEpoch) {
            active = undefined;
          }
        });

      active = { epoch: runEpoch };
      return execution;
    },
    cancel: (): boolean => {
      if (!active || status.state !== 'running') return false;

      epoch += 1;
      active = undefined;
      status = {
        ...status,
        state: 'cancelled',
        finishedAt: new Date().toISOString(),
        error: undefined,
        logs: [...status.logs, 'cancelled'].slice(-MAX_LOG_LINES),
      };

      return true;
    },
  };
}

async function defaultRun(request: RunRequest, log: (line: string) => void): Promise<void> {
  log(`run dependency not configured for ${request.kind}`);
  throw new Error('Run dependency not configured');
}

function cloneStatus(status: RunStatus): RunStatus {
  return {
    ...status,
    request: status.request ? { ...status.request } : undefined,
    logs: [...status.logs],
  };
}

function cloneRequest(request: RunRequest): RunRequest {
  return { ...request };
}
