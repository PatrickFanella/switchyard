import { useCallback, useEffect, useMemo, useState } from 'react';
import { APIError, loadRuntimeConfig, messageFrom, updateRuntimeSetting } from '../api';
import type { AdapterPayload, RuntimeConfigResponse, RuntimeConfigSetting } from '../types';
import { formatTime, HudEmptyState, HudErrorState, HudField, HudLabel, HudLoadingState, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';

const restartLabels: Record<RuntimeConfigSetting['restart'], string> = {
  api: 'restart API',
  opencode: 'restart OpenCode',
  compose: 're-render Compose',
  none: 'no restart',
};

function loadErrorMessage(error: unknown) {
  if (error instanceof APIError && (error.status === 401 || error.status === 403)) {
    return 'The control API rejected this request. Sign in with a read or admin credential to view effective configuration.';
  }
  if (error instanceof APIError && error.status === 404) {
    return 'This API does not expose /api/runtime-config. Upgrade the backend or use the control listener; the model listener never serves configuration.';
  }
  return messageFrom(error);
}

// Secret values are never returned by the API; render only whether they are set.
export function effectiveValueLabel(setting: RuntimeConfigSetting) {
  if (setting.sensitivity === 'secret') {
    return setting.configured ? 'configured (value hidden)' : 'not configured';
  }
  switch (setting.source) {
    case 'not-observable':
      return 'not observable by the API process';
    case 'unavailable':
      return 'component not running';
    case 'unset':
      return 'unset';
    default:
      return setting.effective === '' ? '(empty)' : (setting.effective ?? 'unset');
  }
}

export function hasDrift(setting: RuntimeConfigSetting) {
  return Boolean(setting.overridden) || Boolean(setting.deployment_override);
}

function SettingRow({ setting }: { setting: RuntimeConfigSetting }) {
  const drift = hasDrift(setting);
  return (
    <HudPanel className="bg-[hsl(var(--panel-soft))] p-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <span className="break-all font-mono text-sm font-semibold text-[hsl(var(--ink))]">{setting.key}</span>
          <p className="mt-1 text-xs text-[hsl(var(--muted))]">{setting.description}</p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <span className={`hud-chip px-2 py-0.5 text-[0.6rem] ${setting.sensitivity === 'secret' ? statusClasses('failed') : statusClasses('')}`}>{setting.sensitivity}</span>
          <span className={`hud-chip px-2 py-0.5 text-[0.6rem] ${statusClasses('')}`}>{setting.source}</span>
          {drift ? <span className={`hud-chip px-2 py-0.5 text-[0.6rem] ${statusClasses('queued')}`}>drift</span> : null}
        </div>
      </div>
      <div className="mt-3 grid gap-2 text-xs sm:grid-cols-3">
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Effective</HudLabel>
          <div className="mt-1 break-all text-[hsl(var(--ink))]">{effectiveValueLabel(setting)}</div>
        </div>
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Contract default</HudLabel>
          <div className="mt-1 break-all text-[hsl(var(--ink))]">
            {setting.default === undefined ? 'none' : setting.default === '' ? '(empty)' : setting.default}
            {setting.compose_default !== undefined && setting.compose_default !== setting.default ? ` · Compose ${setting.compose_default || '(empty)'}` : ''}
          </div>
        </div>
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Change</HudLabel>
          <div className="mt-1 text-[hsl(var(--ink))]">{setting.mutability} · {restartLabels[setting.restart]} · {setting.authorization}</div>
        </div>
      </div>
      {setting.deployment_override ? <p className="mt-2 text-xs text-[hsl(var(--warn))]">Deployment difference: {setting.deployment_override}</p> : null}
      <p className="mt-2 text-xs text-[hsl(var(--muted))]" data-testid={`instructions-${setting.key}`}>{setting.instructions}</p>
    </HudPanel>
  );
}

function MutableControl({ setting, busy, onToggle }: { setting: RuntimeConfigSetting; busy: boolean; onToggle: (setting: RuntimeConfigSetting, value: boolean) => void }) {
  const current = setting.effective === 'true';
  const available = setting.available === true;
  return (
    <HudPanel className="bg-[hsl(var(--panel-soft))] p-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <span className="font-mono text-sm font-semibold text-[hsl(var(--ink))]">{setting.key}</span>
          <p className="mt-1 text-xs text-[hsl(var(--muted))]">{setting.description}</p>
        </div>
        <button
          type="button"
          className="hud-chip px-3 py-1.5 text-[0.65rem] disabled:opacity-50"
          disabled={!available || busy}
          aria-pressed={current}
          aria-label={`${current ? 'Resume' : 'Pause'} ${setting.key}`}
          onClick={() => onToggle(setting, !current)}
        >
          {available ? (current ? 'Paused · resume' : 'Active · pause') : 'Unavailable'}
        </button>
      </div>
      {!available ? <p className="mt-2 text-xs text-[hsl(var(--muted))]">The {setting.component} component is not running in this API process, so this control is disabled.</p> : null}
    </HudPanel>
  );
}

export function ConfigPage({ adapters }: { adapters: AdapterPayload }) {
  const [config, setConfig] = useState<RuntimeConfigResponse | null>(null);
  const [loadError, setLoadError] = useState('');
  const [actionError, setActionError] = useState('');
  const [busy, setBusy] = useState(false);
  const [driftOnly, setDriftOnly] = useState(false);
  const [reason, setReason] = useState('');

  const reload = useCallback(async () => {
    try {
      setConfig(await loadRuntimeConfig());
      setLoadError('');
    } catch (error) {
      setLoadError(loadErrorMessage(error));
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  const toggle = useCallback(
    async (setting: RuntimeConfigSetting, value: boolean) => {
      if (!config) return;
      setBusy(true);
      setActionError('');
      try {
        const result = await updateRuntimeSetting(setting.key, value, config.revision, reason.trim());
        if (!result.ok) {
          if (result.status === 403 || result.status === 401) {
            setActionError('Admin authorization is required to change runtime settings. Read-only credentials can view this page but not change it.');
          } else if (result.code === 'stale-revision') {
            setActionError('Another operator changed configuration first. The page was reloaded; review the current value and retry.');
          } else {
            setActionError(result.message);
          }
        } else {
          setReason('');
        }
        await reload();
      } catch (error) {
        setActionError(messageFrom(error));
      } finally {
        setBusy(false);
      }
    },
    [config, reason, reload],
  );

  const groups = useMemo(() => {
    const byComponent = new Map<string, RuntimeConfigSetting[]>();
    for (const setting of config?.settings ?? []) {
      if (setting.mutability === 'runtime-mutable') continue;
      if (driftOnly && !hasDrift(setting)) continue;
      byComponent.set(setting.component, [...(byComponent.get(setting.component) ?? []), setting]);
    }
    return [...byComponent.entries()];
  }, [config, driftOnly]);

  const mutable = (config?.settings ?? []).filter((setting) => setting.mutability === 'runtime-mutable');
  const driftCount = (config?.settings ?? []).filter(hasDrift).length;
  const secretCount = (config?.settings ?? []).filter((setting) => setting.sensitivity === 'secret').length;

  return (
    <HudSection
      kicker="Runtime"
      title="Effective configuration"
      note="Values the running API process resolved, where each came from, and how to change it. Almost every setting is file-managed: edit the deployed environment file and restart. Only the runtime pauses below can be changed here, they need an admin credential, and they reset when the API restarts. Secret values are never sent to the browser."
    >
      <div className="grid gap-3 md:grid-cols-4">
        <HudField label="Environment" value={adapters.config.environment} />
        <HudField label="Version" value={adapters.config.version} />
        <HudField label="Health" value={adapters.health.status} />
        <HudField label="Config revision" value={config ? `${config.revision} · ${formatTime(config.generated_at)}` : 'loading'} />
      </div>

      {loadError ? <HudErrorState className="mt-4" role="alert">{loadError}</HudErrorState> : null}
      {!config && !loadError ? <HudLoadingState className="mt-4">Loading effective configuration…</HudLoadingState> : null}

      {config ? (
        <>
          <HudPanelStrong className="mt-4 p-4">
            <h3 className="hud-label text-[hsl(var(--signal))]">Runtime controls</h3>
            <p className="mt-2 text-xs text-[hsl(var(--muted))]">Changes apply immediately, are audited with your identity, and are rejected if the revision changed since this page loaded.</p>
            <label className="mt-3 block text-xs text-[hsl(var(--muted))]">
              Reason (recorded in the audit list)
              <input className="mt-1 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-1 text-sm text-[hsl(var(--ink))]" value={reason} maxLength={256} onChange={(event) => setReason(event.target.value)} />
            </label>
            {actionError ? <HudErrorState className="mt-3" role="alert">{actionError}</HudErrorState> : null}
            <div className="mt-3 grid gap-3 md:grid-cols-2">
              {mutable.map((setting) => (
                <MutableControl key={setting.key} setting={setting} busy={busy} onToggle={(target, value) => void toggle(target, value)} />
              ))}
            </div>
            <h4 className="hud-label mt-4 text-[hsl(var(--faint))]">Recent runtime changes (this process)</h4>
            {config.changes.length === 0 ? <HudEmptyState className="mt-2">No runtime changes since the API started.</HudEmptyState> : null}
            <ul className="mt-2 grid gap-2">
              {config.changes.map((change) => (
                <li key={change.revision} className="text-xs text-[hsl(var(--muted))]">
                  r{change.revision} · {change.key} {change.previous} → {change.value} by {change.actor} · {formatTime(change.at)}
                  {change.reason ? ` · ${change.reason}` : ''}
                </li>
              ))}
            </ul>
          </HudPanelStrong>

          <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
            <p className="text-xs text-[hsl(var(--muted))]">
              {config.settings.length} settings · {driftCount} differ from the defaults contract · {secretCount} secrets (values hidden)
            </p>
            <label className="flex items-center gap-2 text-xs text-[hsl(var(--muted))]">
              <input type="checkbox" checked={driftOnly} onChange={(event) => setDriftOnly(event.target.checked)} />
              Show drift only
            </label>
          </div>
          {groups.length === 0 ? <HudEmptyState className="mt-3">No settings differ from the defaults contract.</HudEmptyState> : null}
          <div className="mt-3 grid gap-4">
            {groups.map(([component, settings]) => (
              <HudPanelStrong key={component} className="p-4">
                <h3 className="hud-label text-[hsl(var(--signal))]">{component}</h3>
                <div className="mt-3 grid gap-2">
                  {settings.map((setting) => (
                    <SettingRow key={setting.key} setting={setting} />
                  ))}
                </div>
              </HudPanelStrong>
            ))}
          </div>
        </>
      ) : null}

      <HudPanelStrong className="mt-4 p-4">
        <h3 className="hud-label text-[hsl(var(--signal))]">Module health</h3>
        <div className="mt-3 grid gap-3 md:grid-cols-2">
          {adapters.health.services.map((service) => (
            <HudPanel key={service.name} className="bg-[hsl(var(--panel-soft))] p-3">
              <div className="flex items-center justify-between gap-3">
                <span className="text-sm font-semibold text-[hsl(var(--ink))]">{service.name}</span>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(service.status)}`}>{service.status}</span>
              </div>
              <p className="mt-2 text-xs text-[hsl(var(--muted))]">{service.details || 'No details'}</p>
            </HudPanel>
          ))}
        </div>
      </HudPanelStrong>
    </HudSection>
  );
}
