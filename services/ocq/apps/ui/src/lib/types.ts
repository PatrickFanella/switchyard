export type Settings = {
  baseUrl: string
  apiKey: string
  refreshMs: number
  theme: 'dark'
}

export type MetricsSummary = {
  uptimeSeconds: number
  requestsTotal: number
  errorsTotal: number
  activeStreams: number
  p95LatencyMs: number
  latencyBuckets: number[]
}

export type GatewayRequest = {
  ts: string
  route: string
  status: number
  model?: string
  durationMs?: number
  stream?: boolean
  sessionID?: string
  error?: string
}

export type GatewayLogLevel = 'debug' | 'info' | 'warn' | 'error'

export type GatewayLog = {
  ts: string
  level: GatewayLogLevel
  message: string
  meta?: Record<string, unknown>
}

export type SessionSummary = {
  id: string
  title: string
  createdAt: number | null
  updatedAt: number | null
}

export type ChatMessage = {
  id?: string | null
  role: string
  content: string
}

export type SessionDetail = SessionSummary & {
  observeOnly: boolean
  messages: ChatMessage[]
}
