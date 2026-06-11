import { request } from './client'
import type {
  ApiKey, BrokerStatus, CreatedKey, HistoryEntry,
  InflightInfo, LogRecord, QueueSnapshot, UIStatus, AuditEntry,
  LogSearchEntry, RequestSearchEntry, SemanticSearchEntry, StatsSummary,
} from './types'

// ---- public ----
export const getBrokerStatus = () => request<BrokerStatus>('/broker/status', { admin: false })
export const getUIStatus     = () => request<UIStatus>('/ui/api/status', { admin: false })
export const getHistory      = (n = 100) => request<{ entries: HistoryEntry[] }>(`/ui/api/history?n=${n}`, { admin: false })

// ---- admin: queue ----
export const getQueue      = () => request<QueueSnapshot>('/admin/queue')
export const getInflight   = () => request<InflightInfo>('/admin/inflight')
export const dropRequest   = (id: string) => request<void>(`/admin/queue/${encodeURIComponent(id)}`, { method: 'DELETE' })
export const drainQueue    = () => request<{ dropped: number }>('/admin/queue', { method: 'DELETE' })
export const cancelInflight = () => request<void>('/admin/inflight/cancel', { method: 'POST' })

// ---- admin: keys ----
export const listKeys   = () => request<{ keys: ApiKey[] }>('/admin/keys')
export const createKey  = (input: { name: string; priority?: number }) =>
  request<CreatedKey>('/admin/keys', { method: 'POST', body: input })
export const revokeKey  = (name: string) =>
  request<void>(`/admin/keys/${encodeURIComponent(name)}`, { method: 'DELETE' })
export const updateKey  = (name: string, input: { priority: number }) =>
  request<ApiKey>(`/admin/keys/${encodeURIComponent(name)}`, { method: 'PATCH', body: input })

// ---- admin: logs ----
export const getRecentLogs  = (n = 200) => request<{ entries: LogRecord[] }>(`/admin/logs/recent?n=${n}`)
export const getRecentAudit = (n = 200) => request<{ entries: AuditEntry[] }>(`/admin/audit/recent?n=${n}`)

// ---- admin: persisted search/stats ----
export const searchLogs = (params: URLSearchParams) =>
  request<{ entries: LogSearchEntry[] }>(`/admin/search/logs?${params}`)

export const searchRequests = (params: URLSearchParams) =>
  request<{ entries: RequestSearchEntry[] }>(`/admin/search/requests?${params}`)

export const semanticSearch = (input: { q: string; client?: string; model?: string; status?: string; path?: string; limit?: number }) =>
  request<{ entries: SemanticSearchEntry[] }>('/admin/search/semantic', { method: 'POST', body: input })

export const getStatsSummary = (params: URLSearchParams) =>
  request<StatsSummary>(`/admin/stats/summary?${params}`)

export const submitFeedback = (input: { span_id: string; name: string; label?: string; score?: number; explanation?: string }) =>
  request<void>('/admin/feedback', { method: 'POST', body: input })
