import { useEffect, useMemo, useRef, useState } from 'react'
import { Shell, type Tab } from './components/Shell'
import { SettingsPanel } from './components/SettingsPanel'
import { createGatewayClient } from './lib/api'
import { loadSettings, saveSettings } from './lib/storage'
import type { GatewayLog, GatewayRequest, MetricsSummary, SessionDetail, SessionSummary } from './lib/types'
import { Logs } from './pages/Logs'
import { Overview } from './pages/Overview'
import { Requests } from './pages/Requests'
import { Sessions } from './pages/Sessions'

function errorMessage(reason: unknown) {
  return reason instanceof Error ? reason.message : String(reason)
}

export function App() {
  const [tab, setTab] = useState<Tab>('Overview')
  const [settings, setSettings] = useState(() => loadSettings())
  const [metrics, setMetrics] = useState<MetricsSummary>()
  const [requests, setRequests] = useState<GatewayRequest[]>([])
  const [logs, setLogs] = useState<GatewayLog[]>([])
  const [sessions, setSessions] = useState<SessionSummary[]>([])
  const [selectedSession, setSelectedSession] = useState<SessionDetail>()
  const [selectedSessionId, setSelectedSessionId] = useState<string | null>(null)
  const [isRefreshing, setIsRefreshing] = useState(false)
  const [lastRefreshAt, setLastRefreshAt] = useState<number | null>(null)
  const [syncError, setSyncError] = useState<string | null>(null)
  const selectedSessionIdRef = useRef<string | null>(null)

  const refreshMs = Math.max(1000, Number(settings.refreshMs) || 3000)
  const client = useMemo(() => {
    if (!settings.apiKey.trim()) return null
    return createGatewayClient(settings.baseUrl.trim(), settings.apiKey.trim())
  }, [settings.apiKey, settings.baseUrl])

  useEffect(() => {
    saveSettings(settings)
  }, [settings])

  useEffect(() => {
    selectedSessionIdRef.current = selectedSessionId
  }, [selectedSessionId])

  useEffect(() => {
    if (!client) {
      setIsRefreshing(false)
      setSyncError(null)
      return undefined
    }

    let cancelled = false

    const refresh = async () => {
      setIsRefreshing(true)
      const [metricsResult, requestsResult, logsResult, sessionsResult] = await Promise.allSettled([
        client.metrics(),
        client.requests(),
        client.logs(),
        client.sessions(),
      ])

      if (cancelled) return

      if (metricsResult.status === 'fulfilled') setMetrics(metricsResult.value)
      if (requestsResult.status === 'fulfilled') setRequests(requestsResult.value.requests)
      if (logsResult.status === 'fulfilled') setLogs(logsResult.value.logs)
      if (sessionsResult.status === 'fulfilled') setSessions(sessionsResult.value.sessions)

      const failures = [metricsResult, requestsResult, logsResult, sessionsResult].flatMap((result) =>
        result.status === 'rejected' ? [errorMessage(result.reason)] : [],
      )

      setSyncError(failures.length ? failures.join(' · ') : null)
      setLastRefreshAt(Date.now())
      setIsRefreshing(false)

      void refreshSelected(selectedSessionIdRef.current ?? undefined)
    }

    void refresh()
    const timer = window.setInterval(() => {
      void refresh()
    }, refreshMs)

    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [client, refreshMs])

  async function selectSession(id: string) {
    if (!client) return
    selectedSessionIdRef.current = id
    setSelectedSessionId(id)
    setSelectedSession(undefined)
    const detail = await client.session(id)
    if (selectedSessionIdRef.current !== id) return
    setSelectedSession(detail.session)
  }

  async function refreshSelected(id?: string) {
    if (!id || !client) return
    const detail = await client.session(id)
    if (selectedSessionIdRef.current !== id) return
    setSelectedSession(detail.session)
  }

  const connectionState = client
    ? syncError
      ? 'warning'
      : isRefreshing
        ? 'neutral'
        : 'success'
    : 'neutral'

  const connectionLabel = client
    ? syncError
      ? 'partial sync'
      : isRefreshing
        ? 'syncing'
        : 'live'
    : 'unconfigured'

  const statusCopy = client
    ? syncError
      ? 'polling with partial failures'
      : `polling every ${Math.round(refreshMs / 1000)}s`
    : 'set gateway url and api key'

  return (
    <Shell
      tab={tab}
      onTab={setTab}
      connectionLabel={connectionLabel}
      connectionTone={connectionState}
      statusCopy={statusCopy}
    >
      {tab === 'Overview' && (
        <Overview
          metrics={metrics}
          isRefreshing={isRefreshing}
          syncError={syncError}
          lastRefreshAt={lastRefreshAt}
          refreshMs={refreshMs}
        />
      )}
      {tab === 'Requests' && <Requests requests={requests} isRefreshing={isRefreshing} syncError={syncError} />}
      {tab === 'Logs' && <Logs logs={logs} isRefreshing={isRefreshing} syncError={syncError} />}
      {tab === 'Sessions' && (
        <Sessions
          sessions={sessions}
          selected={selectedSession}
          selectedId={selectedSessionId}
          onSelect={selectSession}
          client={client}
          refreshSelected={refreshSelected}
          isRefreshing={isRefreshing}
          syncError={syncError}
        />
      )}
      {tab === 'Settings' && <SettingsPanel settings={settings} onChange={setSettings} />}
    </Shell>
  )
}
