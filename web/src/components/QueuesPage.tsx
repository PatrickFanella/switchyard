import { useCallback, useEffect, useMemo, useState } from 'react';
import { APIError, cancelModelBrokerRequest, loadModelBrokerHistory, loadModelBrokerRequest, messageFrom } from '../api';
import type { ModelBrokerRequestHistoryItem } from '../types';
import { formatTime, HudChip, HudEmptyState, HudErrorState, HudField, HudLabel, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';

const activeStatuses = new Set(['queued', 'running', 'cancel_requested']);

function durationMS(request: ModelBrokerRequestHistoryItem) {
  if (!request.started_at || !request.finished_at) {
    return 0;
  }
  const started = new Date(request.started_at).getTime();
  const finished = new Date(request.finished_at).getTime();
  if (!Number.isFinite(started) || !Number.isFinite(finished) || finished < started) {
    return 0;
  }
  return finished - started;
}

function requestMatches(request: ModelBrokerRequestHistoryItem, query: string, status: string, errorOnly: boolean) {
  const normalized = query.trim().toLowerCase();
  const haystack = `${request.id} ${request.model} ${request.path} ${request.client} ${request.status} ${request.error ?? ''}`.toLowerCase();
  return (status === 'all' || request.status === status) && (!errorOnly || request.status === 'failed' || Boolean(request.error)) && (!normalized || haystack.includes(normalized));
}

function RequestCard({ request, selected, onSelect }: { request: ModelBrokerRequestHistoryItem; selected: boolean; onSelect: (request: ModelBrokerRequestHistoryItem) => void }) {
  return (
    <button
      className={`hud-panel w-full bg-[hsl(var(--panel-soft))] p-3 text-left transition hover:border-[hsl(var(--signal))] ${selected ? 'border-[hsl(var(--signal))]' : ''}`.trim()}
      onClick={() => onSelect(request)}
      type="button"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">{request.id}</HudLabel>
          <div className="mt-2 text-sm font-semibold text-[hsl(var(--ink))]">{request.model || 'unknown model'}</div>
        </div>
        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
      </div>
      <p className="mt-3 break-all text-xs text-[hsl(var(--faint))]">
        {request.path || 'unknown path'} · {durationMS(request)}ms · {formatTime(request.created_at)}
      </p>
    </button>
  );
}

function RequestDetail({ request, onCancel, canceling }: { request: ModelBrokerRequestHistoryItem | null; onCancel: (requestID: string) => void; canceling: boolean }) {
  if (!request) {
    return <HudEmptyState>Select a request to inspect model, route, timing, status, and correlation IDs.</HudEmptyState>;
  }

  const correlations = Object.entries(request.correlation_ids ?? {});

  return (
    <HudPanelStrong className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Selected request</HudLabel>
          <h3 className="mt-2 break-all text-xl font-semibold text-[hsl(var(--ink))]">{request.id}</h3>
        </div>
        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
      </div>

      <div className="mt-4 grid gap-3 sm:grid-cols-2">
        <HudField label="Model" value={request.model || 'unknown'} />
        <HudField label="Path" value={request.path || 'unknown'} />
        <HudField label="Client" value={request.client || 'unknown'} />
        <HudField label="Duration" value={`${durationMS(request)}ms`} />
        <HudField label="Created" value={formatTime(request.created_at)} />
        <HudField label="Updated" value={formatTime(request.updated_at)} />
      </div>

      {request.error ? <HudErrorState className="mt-4">{request.error}</HudErrorState> : null}

      <div className="mt-4">
        <HudLabel className="text-[hsl(var(--signal))]">Correlation IDs</HudLabel>
        {correlations.length === 0 ? <HudEmptyState className="mt-3">No correlation IDs recorded.</HudEmptyState> : null}
        <div className="mt-3 grid gap-2">
          {correlations.map(([key, value]) => (
            <HudPanel key={key} className="bg-[hsl(var(--panel-soft))] px-3 py-2">
              <span className="text-xs text-[hsl(var(--faint))]">{key}</span>
              <p className="mt-1 break-all text-sm text-[hsl(var(--ink))]">{value}</p>
            </HudPanel>
          ))}
        </div>
      </div>

      {activeStatuses.has(request.status) ? (
        <HudChip className="mt-4 border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--danger))]" disabled={canceling} onClick={() => onCancel(request.id)} type="button">
          {canceling ? 'Canceling…' : 'Cancel request'}
        </HudChip>
      ) : null}
    </HudPanelStrong>
  );
}

export function QueuesPage({ selectedRequestID, setSelectedRequestID }: { selectedRequestID: string; setSelectedRequestID: (requestID: string) => void }) {
  const [requests, setRequests] = useState<ModelBrokerRequestHistoryItem[]>([]);
  const [selected, setSelected] = useState<ModelBrokerRequestHistoryItem | null>(null);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('all');
  const [errorOnly, setErrorOnly] = useState(false);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [cancelingID, setCancelingID] = useState('');
  const [error, setError] = useState('');

  const refresh = useCallback(async () => {
    setRefreshing(true);
    try {
      const history = await loadModelBrokerHistory(query);
      setRequests(history);
      setError('');
      if (selectedRequestID) {
        let detail = history.find((request) => request.id === selectedRequestID) ?? null;
        if (!detail) {
          try {
            detail = await loadModelBrokerRequest(selectedRequestID);
          } catch (err) {
            if (!(err instanceof APIError && err.status === 404)) throw err;
            setError(`Request ${selectedRequestID} is not in model broker history. It is unknown or was removed by retention.`);
          }
        }
        setSelected(detail);
      } else {
        setSelected((current) => (current ? (history.find((request) => request.id === current.id) ?? current) : null));
      }
    } catch (err) {
      setError(messageFrom(err));
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [query, selectedRequestID]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const filtered = useMemo(() => requests.filter((request) => requestMatches(request, query, status, errorOnly)), [errorOnly, query, requests, status]);
  const totals = useMemo(() => requests.reduce<Record<string, number>>((acc, request) => ({ ...acc, [request.status]: (acc[request.status] ?? 0) + 1 }), {}), [requests]);

  const selectRequest = (request: ModelBrokerRequestHistoryItem) => {
    setSelected(request);
    setSelectedRequestID(request.id);
  };

  const cancelRequest = async (requestID: string) => {
    setCancelingID(requestID);
    try {
      await cancelModelBrokerRequest(requestID);
      await refresh();
    } catch (err) {
      setError(messageFrom(err));
    } finally {
      setCancelingID('');
    }
  };

  return (
    <HudSection kicker="ModelBroker" title="Requests cockpit" note="Durable llama-line/Ollama/OpenAI compatibility requests, filters, and correlation IDs.">
      <div className="grid gap-3 md:grid-cols-[1fr_auto_auto_auto]">
        <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
          <span className="hud-label text-[hsl(var(--signal))]">Search</span>
          <input className="mt-2 w-full bg-transparent text-sm text-[hsl(var(--ink))] outline-none" onChange={(event) => setQuery(event.target.value)} placeholder="request, model, route, client" value={query} />
        </label>
        <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
          <span className="hud-label text-[hsl(var(--signal))]">Status</span>
          <select className="mt-2 bg-[hsl(var(--panel-soft))] text-sm text-[hsl(var(--ink))]" onChange={(event) => setStatus(event.target.value)} value={status}>
            {['all', 'queued', 'running', 'completed', 'failed', 'canceled', 'cancel_requested'].map((item) => <option key={item}>{item}</option>)}
          </select>
        </label>
        <HudChip className={`${errorOnly ? 'border-[hsl(var(--danger))] text-[hsl(var(--danger))]' : 'text-[hsl(var(--muted))]'} px-3 py-2 text-[0.62rem]`} onClick={() => setErrorOnly((value) => !value)} type="button">
          Error only
        </HudChip>
        <HudChip className="px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]" disabled={refreshing} onClick={() => void refresh()} type="button">
          {refreshing ? 'Refreshing…' : 'Refresh'}
        </HudChip>
      </div>

      {error ? <HudErrorState className="mt-4">{error}</HudErrorState> : null}

      <div className="mt-4 grid gap-3 md:grid-cols-5">
        {['queued', 'running', 'completed', 'failed', 'canceled'].map((item) => <HudField key={item} label={item} value={totals[item] ?? 0} />)}
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(360px,0.9fr)]">
        <HudPanelStrong className="p-4">
          <div className="flex items-center justify-between gap-3">
            <HudLabel className="text-[hsl(var(--signal))]">Request lanes</HudLabel>
            <span className="text-xs text-[hsl(var(--faint))]">{filtered.length} visible</span>
          </div>
          <div className="mt-4 grid gap-3">
            {loading ? <HudEmptyState>Loading broker history…</HudEmptyState> : null}
            {!loading && filtered.length === 0 ? <HudEmptyState>No requests match the current filters.</HudEmptyState> : null}
            {filtered.map((request) => <RequestCard key={request.id} onSelect={selectRequest} request={request} selected={selected?.id === request.id} />)}
          </div>
        </HudPanelStrong>

        <RequestDetail canceling={cancelingID === selected?.id} onCancel={cancelRequest} request={selected} />
      </div>
    </HudSection>
  );
}
