import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import * as api from '@/lib/api/endpoints'

const KEYS = {
  queue:    ['queue'] as const,
  inflight: ['inflight'] as const,
  keys:     ['keys'] as const,
  history:  ['history'] as const,
  broker:   ['broker', 'status'] as const,
  uiStatus: ['ui', 'status'] as const,
  logs:     ['logs', 'recent'] as const,
  audit:    ['audit', 'recent'] as const,
  stats:    ['stats', 'summary'] as const,
}

export function useQueue()    { return useQuery({ queryKey: KEYS.queue,    queryFn: api.getQueue,    refetchInterval: 2000 }) }
export function useInflight() { return useQuery({ queryKey: KEYS.inflight, queryFn: api.getInflight, refetchInterval: 2000 }) }
export function useKeys()     { return useQuery({ queryKey: KEYS.keys,     queryFn: api.listKeys }) }
export function useHistory(n = 100) { return useQuery({ queryKey: [...KEYS.history, n], queryFn: () => api.getHistory(n), refetchInterval: 5000 }) }
export function useBrokerStatus()   { return useQuery({ queryKey: KEYS.broker,   queryFn: api.getBrokerStatus,   refetchInterval: 5000 }) }
export function useUIStatus()       { return useQuery({ queryKey: KEYS.uiStatus, queryFn: api.getUIStatus,       refetchInterval: 2000 }) }
export function useRecentLogs(n = 200)  { return useQuery({ queryKey: [...KEYS.logs, n],  queryFn: () => api.getRecentLogs(n) }) }
export function useRecentAudit(n = 200) { return useQuery({ queryKey: [...KEYS.audit, n], queryFn: () => api.getRecentAudit(n) }) }
export function useStatsSummary(range = '1h') {
  return useQuery({
    queryKey: [...KEYS.stats, range],
    queryFn: () => {
      const until = new Date()
      const ms = range === '24h' ? 86_400_000 : range === '6h' ? 21_600_000 : 3_600_000
      const since = new Date(until.getTime() - ms)
      const p = new URLSearchParams({ since: since.toISOString(), until: until.toISOString() })
      return api.getStatsSummary(p)
    },
    refetchInterval: 5000,
  })
}

export function useDropRequest() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.dropRequest(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: KEYS.queue })
      qc.invalidateQueries({ queryKey: KEYS.uiStatus })
      qc.invalidateQueries({ queryKey: KEYS.broker })
    },
  })
}
export function useDrainQueue() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: api.drainQueue,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: KEYS.queue })
      qc.invalidateQueries({ queryKey: KEYS.uiStatus })
      qc.invalidateQueries({ queryKey: KEYS.broker })
    },
  })
}
export function useCancelInflight() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: api.cancelInflight,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: KEYS.inflight })
      qc.invalidateQueries({ queryKey: KEYS.uiStatus })
      qc.invalidateQueries({ queryKey: KEYS.broker })
    },
  })
}
export function useCreateKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { name: string; priority?: number }) => api.createKey(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.keys }),
  })
}
export function useRevokeKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => api.revokeKey(name),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.keys }),
  })
}
export function useUpdateKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, priority }: { name: string; priority: number }) => api.updateKey(name, { priority }),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.keys }),
  })
}
