import type {
  ApiErrorPayload,
  PostDetail,
  PostSummary,
  RunRequest,
  RunStatus,
  RuntimeConfig,
  ServiceAction,
  ServiceExecutionResult,
} from './types';

const TOKEN_STORAGE_KEY = 'changemaker.adminToken';
const DEFAULT_BASE_URL = (import.meta.env.VITE_API_BASE_URL as string | undefined)?.trim() ?? '';

export class ApiError extends Error {
  status: number;
  payload: unknown;

  constructor(message: string, status: number, payload: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.payload = payload;
  }
}

export type ApiClientOptions = {
  baseUrl?: string;
  token: string;
  onUnauthorized?: () => void;
};

export type ApiClient = ReturnType<typeof createApi>;

export function readStoredToken(): string {
  if (typeof window === 'undefined') return '';
  return window.localStorage.getItem(TOKEN_STORAGE_KEY) ?? '';
}

export function saveStoredToken(token: string): void {
  if (typeof window === 'undefined') return;
  window.localStorage.setItem(TOKEN_STORAGE_KEY, token);
}

export function clearStoredToken(): void {
  if (typeof window === 'undefined') return;
  window.localStorage.removeItem(TOKEN_STORAGE_KEY);
}

export function createApi(options: ApiClientOptions) {
  const baseUrl = options.baseUrl?.trim() ?? DEFAULT_BASE_URL;
  const token = options.token.trim();

  async function requestJson<T>(path: string, init: RequestInit = {}): Promise<T> {
    const response = await fetch(resolveUrl(baseUrl, path), {
      ...init,
      headers: buildHeaders(token, init.headers, Boolean(init.body)),
    });

    if (response.status === 204) {
      return undefined as T;
    }

    const payload = await parseResponse(response);
    if (!response.ok) {
      if (response.status === 401) options.onUnauthorized?.();
      throw new ApiError(extractMessage(payload) ?? response.statusText ?? 'Request failed', response.status, payload);
    }

    return payload as T;
  }

  async function requestText(path: string, init: RequestInit = {}): Promise<string> {
    const response = await fetch(resolveUrl(baseUrl, path), {
      ...init,
      headers: buildHeaders(token, init.headers, Boolean(init.body)),
    });

    const payload = await response.text();
    if (!response.ok) {
      if (response.status === 401) options.onUnauthorized?.();
      throw new ApiError(payload || response.statusText || 'Request failed', response.status, payload);
    }

    return payload;
  }

  return {
    listPosts: () => requestJson<PostSummary[]>('/api/posts'),
    readPost: (fileName: string) => requestJson<PostDetail>(`/api/posts/${encodeURIComponent(fileName)}`),
    readRuntimeConfig: () => requestJson<RuntimeConfig>('/api/runtime-config'),
    saveRuntimeConfig: (config: RuntimeConfig) =>
      requestJson<RuntimeConfig>('/api/runtime-config', {
        method: 'PUT',
        body: JSON.stringify(config),
      }),
    readPrompt: (fileName: string) => requestText(`/api/prompts/${encodeURIComponent(fileName)}`),
    savePrompt: (fileName: string, content: string) =>
      requestJson<{ ok: true }>(`/api/prompts/${encodeURIComponent(fileName)}`, {
        method: 'PUT',
        body: JSON.stringify({ content }),
      }),
    readRunStatus: () => requestJson<RunStatus>('/api/runs/status'),
    startRun: (request: RunRequest) =>
      requestJson<RunStatus>('/api/runs', {
        method: 'POST',
        body: JSON.stringify(request),
      }),
    readServiceStatus: () => requestJson<ServiceExecutionResult>('/api/service/service-status'),
    readTimerStatus: () => requestJson<ServiceExecutionResult>('/api/service/timer-status'),
    invokeServiceAction: (action: Exclude<ServiceAction, 'service-status' | 'timer-status'>) =>
      requestJson<ServiceExecutionResult>(`/api/service/${action}`, {
        method: 'POST',
        body: JSON.stringify({ confirm: true, confirmationText: action }),
      }),
  };
}

function resolveUrl(baseUrl: string, path: string): string {
  if (!baseUrl) return path;
  return new URL(path, baseUrl).toString();
}

function buildHeaders(token: string, headers: HeadersInit | undefined, hasBody: boolean): Headers {
  const merged = new Headers(headers);
  merged.set('authorization', `Bearer ${token}`);
  if (hasBody && !merged.has('content-type')) {
    merged.set('content-type', 'application/json');
  }
  return merged;
}

async function parseResponse(response: Response): Promise<unknown> {
  const contentType = response.headers.get('content-type') ?? '';

  if (contentType.includes('application/json')) {
    return response.json();
  }

  const text = await response.text();
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}

function extractMessage(payload: unknown): string | undefined {
  if (typeof payload === 'string' && payload.trim()) return payload;
  if (isObject(payload) && typeof payload.error === 'string' && payload.error.trim()) return payload.error;
  return undefined;
}

function isObject(value: unknown): value is ApiErrorPayload {
  return typeof value === 'object' && value !== null;
}
