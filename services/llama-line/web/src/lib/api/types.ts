// API response types matching internal/server/*.go

export interface BrokerStatus {
  queue_depth: number
  max_depth: number
  in_flight_client?: string
  in_flight_id?: string
  connected_clients?: number
  queue?: QueueItem[]
  upstreams: UpstreamStatus[]
  cache_stats?: Array<{
    client: string
    hits: number
    misses: number
    size: number
  }>
}

export interface UpstreamStatus {
  name: string
  url: string
  healthy?: boolean
  last_probe_at?: string
  last_probe_ms?: number
  last_probe_error?: string
  consecutive_failures?: number
  models?: string[]
  queue_depth: number
  max_depth: number
  in_flight_client?: string
  in_flight_id?: string
  in_flight_priority?: number
  in_flight_enqueued_at?: string
  in_flight_started_at?: string
  in_flight_job_seconds?: number
  in_flight_total_seconds?: number
  queue: QueueItem[]
}

export interface QueueItem {
  id: string
  client: string
  priority: number
  position: number
  enqueued_at: string
  wait_seconds?: number
}

export interface QueueSnapshot {
  depth: number
  max_depth: number
  items: QueueItem[]
}

export interface InflightInfo {
  in_flight: boolean
  id?: string
  client?: string
  priority?: number
  enqueued_at?: string
  started_at?: string
  job_seconds?: number
  total_seconds?: number
}

export interface ApiKey {
  name: string
  priority: number
  created_at: string
}

export interface CreatedKey extends ApiKey {
  key: string
}

export interface HistoryEntry {
  id: string
  client: string
  model: string
  path: string
  status: string
  duration_ms: number
  prompt_tokens?: number
  completion_tokens?: number
  queue_ms?: number
  upstream_ms?: number
  enqueued_at: string
  started_at: string
  completed_at: string
  error?: string
}

export interface UIStatus {
  queue_depth: number
  max_depth: number
  in_flight_id?: string
  in_flight_client?: string
  queue: Array<{
    id: string
    client: string
    priority: number
    position: number
    enqueued_at: string
  }>
}

export type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

export interface LogRecord {
  time: string
  level: LogLevel
  msg: string
  fields?: Record<string, unknown>
  raw: string
}

export interface AuditEntry {
  time: string
  level: string
  msg: string
  client?: string
  model?: string
  path?: string
  id?: string
  status?: string
  duration_ms?: number
  queue_ms?: number
  upstream_ms?: number
  request_body_bytes?: number
  response_body_bytes?: number
  response_json_valid?: boolean
  request_message_count?: number
  request_tool_names?: string[]
  request_tool_error_count?: number
  request_tool_errors?: string[]
  request_body?: string
  response_body?: string
  [k: string]: unknown
}

export interface LogSearchEntry {
  id: number
  ts: string
  level: LogLevel
  msg: string
  fields: Record<string, unknown>
  raw_json: string
  client?: string
  request_id?: string
  model?: string
  path?: string
}

export interface RequestSearchEntry {
  request_id: string
  ts: string
  client: string
  model: string
  path: string
  upstream?: string
  status: string
  streaming: boolean
  duration_ms: number
  prompt_tokens?: number
  completion_tokens?: number
  queue_ms: number
  upstream_ms: number
  request_body_text?: string
  response_body_text?: string
  request_body_bytes: number
  response_body_bytes: number
  response_json_valid?: boolean
  request_message_count: number
  tool_names?: string[]
  tool_error_count: number
  tool_errors?: string[]
  raw_audit?: unknown
}

export interface SemanticSearchEntry {
  request_id: string
  ts: string
  client: string
  model: string
  path: string
  status: string
  role: string
  content: string
  score: number
}

export interface StatsSummary {
  total_requests: number
  success_count: number
  error_count: number
  cache_hit_count: number
  tool_error_count: number
  malformed_count: number
  total_prompt_tokens: number
  total_completion_tokens: number
  p50_ms: number
  p95_ms: number
  p99_ms: number
  queue_p95_ms: number
  upstream_p95_ms: number
}
