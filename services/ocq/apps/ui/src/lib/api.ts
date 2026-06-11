import type { GatewayLog, GatewayRequest, MetricsSummary, SessionDetail, SessionSummary } from './types'

export class GatewayError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = 'GatewayError'
    this.status = status
  }
}

type JsonObject = Record<string, unknown>

function normalizeBaseUrl(baseUrl: string) {
  return baseUrl.replace(/\/+$/, '')
}

function mergeHeaders(initHeaders?: HeadersInit) {
  const entries = initHeaders ? new Headers(initHeaders) : new Headers()
  return Object.fromEntries(entries.entries())
}

async function parseBody(response: Response) {
  const text = await response.text()
  if (!text) return undefined

  try {
    return JSON.parse(text) as JsonObject
  } catch {
    return { message: text }
  }
}

export function createGatewayClient(baseUrl: string, apiKey: string) {
  const root = normalizeBaseUrl(baseUrl)

  async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const response = await fetch(`${root}${path}`, {
      ...init,
      headers: {
        ...mergeHeaders(init.headers),
        authorization: `Bearer ${apiKey}`,
        'content-type': 'application/json',
      },
    })

    const data = await parseBody(response)
    if (!response.ok) {
      const message =
        typeof data?.error === 'object' && data.error && 'message' in data.error
          ? String((data.error as JsonObject).message || `HTTP ${response.status}`)
          : typeof data?.message === 'string'
            ? data.message
            : `HTTP ${response.status}`
      throw new GatewayError(message, response.status)
    }

    return data as T
  }

  return {
    metrics: () => request<MetricsSummary>('/ocq/metrics'),
    requests: () => request<{ requests: GatewayRequest[] }>('/ocq/requests'),
    logs: () => request<{ logs: GatewayLog[] }>('/ocq/logs'),
    sessions: () => request<{ sessions: SessionSummary[] }>('/ocq/sessions'),
    session: (id: string) =>
      request<{ session: SessionDetail; mode: string; transport: string }>(`/ocq/sessions/${encodeURIComponent(id)}`),
    sendSessionMessage: (id: string, prompt: string) =>
      request<{ sessionID: string; messageID?: string; text: string }>(`/ocq/sessions/${encodeURIComponent(id)}/messages`, {
        method: 'POST',
        body: JSON.stringify({ prompt }),
      }),
  }
}

export type GatewayClient = ReturnType<typeof createGatewayClient>
