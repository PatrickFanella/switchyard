import { useCallback, useEffect, useRef, useState } from 'react';
import { arrayOrEmpty, messageFrom } from '../api';
import { confirmNavigation, registerNavigationGuard } from '../navigationGuard';
import {
  activatableDraft,
  activateProfileVersion,
  loadProfileAudit,
  loadProfileDetail,
  loadProfileListing,
  loadProfileVersions,
  profileDraftFingerprint,
  profileDraftFrom,
  profileExecutors,
  profileVersionLabel,
  rollbackCandidates,
  rollbackProfileVersion,
  saveProfileDraft,
  validateProfileDraft,
  type AgentProfileAuditEntry,
  type AgentProfileIdentity,
  type AgentProfileVersion,
  type ProfileDraft,
  type ProfileListEntry,
  type ProfileMutationResult,
} from '../profileVersions';
import type { AgentProfile } from '../types';
import { loadControlAccess, type ControlAccess } from '../workflowDrafts';
import { formatTime, HudEmptyState, HudField, HudLabel, HudPanel, HudPanelStrong, HudSection } from './hud';

const fieldClass = 'mt-1 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-1 text-sm text-[hsl(var(--ink))] disabled:opacity-70';
const chipClass = 'hud-chip px-3 py-1.5 text-[0.65rem] disabled:opacity-50';
const labelClass = 'block text-xs text-[hsl(var(--muted))]';

export const unsavedProfileMessage = 'You have unsaved changes to this profile draft. Leave and discard them?';

type Detail = { profile: AgentProfile; identity: AgentProfileIdentity; versions: AgentProfileVersion[]; audit: AgentProfileAuditEntry[] };
type Feedback = { tone: 'error' | 'ok'; message: string };

function shortHash(value?: string) {
  return value ? value.slice(0, 12) : 'none';
}

function modelList(models?: string[]) {
  const list = arrayOrEmpty(models);
  return list.length > 0 ? list.join(', ') : 'any model';
}

function ProfileDraftEditor({
  profileID,
  initial,
  expectedVersion,
  onSaved,
  onCancel,
  onForbidden,
  onReload,
}: {
  profileID: string;
  initial: ProfileDraft;
  expectedVersion: number;
  onSaved: (result: Extract<ProfileMutationResult, { ok: true }>) => void;
  onCancel: () => void;
  onForbidden: () => void;
  onReload: () => void;
}) {
  const [draft, setDraft] = useState(initial);
  const [baseline] = useState(() => profileDraftFingerprint(initial));
  const [saving, setSaving] = useState(false);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [stale, setStale] = useState(false);
  const feedbackRef = useRef<HTMLDivElement>(null);
  const dirty = profileDraftFingerprint(draft) !== baseline;
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;

  useEffect(() => {
    const unregister = registerNavigationGuard(() => !dirtyRef.current || window.confirm(unsavedProfileMessage));
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!dirtyRef.current) return;
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => {
      unregister();
      window.removeEventListener('beforeunload', beforeUnload);
    };
  }, []);

  useEffect(() => {
    if (feedback) feedbackRef.current?.focus();
  }, [feedback]);

  const update = (patch: Partial<ProfileDraft>) => setDraft((current) => ({ ...current, ...patch }));
  const toggleExecutor = (executor: string) =>
    setDraft((current) => ({ ...current, allowedExecutors: current.allowedExecutors.includes(executor) ? current.allowedExecutors.filter((item) => item !== executor) : [...current.allowedExecutors, executor] }));

  const save = async () => {
    const problems = validateProfileDraft(draft);
    if (problems.length > 0) {
      setFeedback({ tone: 'error', message: problems.join(' ') });
      return;
    }
    setSaving(true);
    const result = await saveProfileDraft(profileID, draft, expectedVersion);
    setSaving(false);
    if (result.ok) {
      dirtyRef.current = false;
      onSaved(result);
      return;
    }
    if (result.kind === 'forbidden') onForbidden();
    if (result.kind === 'conflict') setStale(true);
    setFeedback({ tone: 'error', message: result.message });
  };

  const cancel = () => {
    if (dirtyRef.current && !window.confirm(unsavedProfileMessage)) return;
    dirtyRef.current = false;
    onCancel();
  };

  return (
    <HudPanel className="mt-4 bg-[hsl(var(--panel-soft))] p-4">
      <form
        aria-label={`Draft for ${profileID}`}
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        <HudLabel className="text-[hsl(var(--signal))]">New draft from version {expectedVersion === 0 ? 'static default' : `v${expectedVersion}`}</HudLabel>
        <p className="mt-2 text-xs text-[hsl(var(--muted))]">Saving creates an immutable draft version. It does not serve runs until it is activated.</p>
        <div className="mt-3 grid gap-3 md:grid-cols-2">
          <label className={labelClass}>
            Name
            <input className={fieldClass} disabled={saving} value={draft.name} onChange={(event) => update({ name: event.target.value })} />
          </label>
          <label className={labelClass}>
            Role
            <input className={fieldClass} disabled={saving} value={draft.role} onChange={(event) => update({ role: event.target.value })} />
          </label>
          <label className={labelClass}>
            Default model
            <input className={fieldClass} disabled={saving} value={draft.defaultModel} onChange={(event) => update({ defaultModel: event.target.value })} />
          </label>
          <label className={labelClass}>
            Allowed models (one per line; empty allows any)
            <textarea className={fieldClass} disabled={saving} rows={3} value={draft.allowedModels} onChange={(event) => update({ allowedModels: event.target.value })} />
          </label>
        </div>
        <fieldset className="mt-3">
          <legend className={labelClass}>Allowed executors</legend>
          <div className="mt-1 flex flex-wrap gap-3">
            {profileExecutors.map((executor) => (
              <label key={executor} className="flex items-center gap-1 text-sm text-[hsl(var(--ink))]">
                <input checked={draft.allowedExecutors.includes(executor)} disabled={saving} type="checkbox" onChange={() => toggleExecutor(executor)} />
                {executor}
              </label>
            ))}
          </div>
        </fieldset>
        <label className={`${labelClass} mt-3`}>
          Prompt contract
          <textarea className={fieldClass} disabled={saving} rows={5} value={draft.promptContract} onChange={(event) => update({ promptContract: event.target.value })} />
        </label>
        <label className={`${labelClass} mt-3`}>
          Note (stored in provenance)
          <input className={fieldClass} disabled={saving} value={draft.note} onChange={(event) => update({ note: event.target.value })} />
        </label>
        {feedback ? (
          <div ref={feedbackRef} tabIndex={-1} role="alert" className="mt-3 border border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] p-3 text-sm text-[hsl(var(--danger))]">
            {feedback.message}
            {stale ? (
              <button className={`${chipClass} ml-3`} onClick={onReload} type="button">
                Reload latest version
              </button>
            ) : null}
          </div>
        ) : null}
        <div className="mt-4 flex flex-wrap gap-2">
          <button className={`${chipClass} border-[hsl(var(--signal))] text-[hsl(var(--signal))]`} disabled={saving || !dirty} type="submit">
            {saving ? 'Saving…' : 'Save draft'}
          </button>
          <button className={chipClass} disabled={saving} onClick={cancel} type="button">
            Cancel
          </button>
          {!dirty ? <span className="self-center text-xs text-[hsl(var(--faint))]">No changes yet.</span> : null}
        </div>
      </form>
    </HudPanel>
  );
}

function VersionRow({ version, identity, canWrite, busy, onRollback }: { version: AgentProfileVersion; identity: AgentProfileIdentity; canWrite: boolean; busy: boolean; onRollback?: (version: number) => void }) {
  const provenance = version.provenance;
  return (
    <li className="hud-panel bg-[hsl(var(--panel-soft))] p-3" data-testid={`profile-version-${version.version}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-semibold text-[hsl(var(--ink))]">
          v{version.version} · {version.status}
          {version.version === identity.active_version ? ' · serving' : ''}
        </span>
        {onRollback ? (
          <button aria-label={`Roll back to version ${version.version}`} className={chipClass} disabled={!canWrite || busy} onClick={() => onRollback(version.version)} type="button">
            Roll back to v{version.version}
          </button>
        ) : null}
      </div>
      <p className="mt-1 text-xs text-[hsl(var(--muted))]">
        Default {version.default_model || 'none'} · models {modelList(version.allowed_models)} · executors {arrayOrEmpty(version.allowed_executors).join(', ')}
      </p>
      <p className="mt-1 text-xs text-[hsl(var(--faint))]">
        {provenance?.origin ?? 'operator'}
        {provenance?.actor ? ` by ${provenance.actor}` : ''}
        {provenance?.derived_from_version ? ` from v${provenance.derived_from_version}` : ''}
        {provenance?.rolled_back_from_version ? ` restoring v${provenance.rolled_back_from_version}` : ''} · created {formatTime(version.created_at)}
        {version.activated_at ? ` · activated ${formatTime(version.activated_at)}` : ''} · contract {shortHash(version.prompt_contract_sha256)}
        {provenance?.note ? ` · “${provenance.note}”` : ''}
      </p>
    </li>
  );
}

export function ProfilesPage() {
  const [entries, setEntries] = useState<ProfileListEntry[]>([]);
  const [access, setAccess] = useState<ControlAccess>({ role: 'unknown', can_write: false });
  const [listError, setListError] = useState('');
  const [loading, setLoading] = useState(true);
  const [selectedID, setSelectedID] = useState('');
  const [detail, setDetail] = useState<Detail | null>(null);
  const [detailError, setDetailError] = useState('');
  const [editing, setEditing] = useState<{ initial: ProfileDraft; expectedVersion: number; key: number } | null>(null);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const canWrite = access.can_write;

  const loadDetail = useCallback(async (id: string) => {
    setDetailError('');
    try {
      const [{ profile, identity }, versions, audit] = await Promise.all([loadProfileDetail(id), loadProfileVersions(id), loadProfileAudit(id)]);
      setDetail({ profile, identity, versions, audit });
      return identity;
    } catch (error) {
      setDetail(null);
      setDetailError(messageFrom(error));
      return undefined;
    }
  }, []);

  const loadList = useCallback(async () => {
    try {
      const listing = await loadProfileListing();
      setEntries(listing);
      setListError('');
      return listing;
    } catch (error) {
      setListError(messageFrom(error));
      return [];
    }
  }, []);

  useEffect(() => {
    let active = true;
    void (async () => {
      const [listing, controlAccess] = await Promise.all([loadList(), loadControlAccess()]);
      if (!active) return;
      setAccess(controlAccess);
      setLoading(false);
      if (listing.length > 0) setSelectedID((current) => current || listing[0].id);
    })();
    return () => {
      active = false;
    };
  }, [loadList]);

  useEffect(() => {
    if (selectedID) void loadDetail(selectedID);
  }, [loadDetail, selectedID]);

  const select = (id: string) => {
    if (id === selectedID) return;
    if (!confirmNavigation()) return;
    setEditing(null);
    setFeedback(null);
    setReason('');
    setSelectedID(id);
  };

  const refreshAfterChange = async (message: string) => {
    setEditing(null);
    setReason('');
    setFeedback({ tone: 'ok', message });
    await Promise.all([loadList(), loadDetail(selectedID)]);
  };

  const handleResult = async (result: ProfileMutationResult, success: string) => {
    if (result.ok) {
      await refreshAfterChange(success);
      return;
    }
    if (result.kind === 'forbidden') setAccess({ role: 'read', can_write: false });
    setFeedback({ tone: 'error', message: result.message });
    if (result.kind === 'conflict') await loadDetail(selectedID);
  };

  const activate = async (version: number) => {
    setBusy(true);
    setFeedback(null);
    const result = await activateProfileVersion(selectedID, version, reason);
    setBusy(false);
    await handleResult(result, `Activated v${version} of ${selectedID}. New runs capture it; existing runs keep their snapshot.`);
  };

  const rollback = async (version: number) => {
    setBusy(true);
    setFeedback(null);
    const result = await rollbackProfileVersion(selectedID, version, reason);
    setBusy(false);
    await handleResult(result, result.ok ? `Restored v${version} of ${selectedID} as v${result.version.version}. Existing runs keep their snapshot.` : '');
  };

  const draftVersion = detail ? activatableDraft(detail.versions, detail.identity) : undefined;
  const rollbacks = detail ? rollbackCandidates(detail.versions, detail.identity) : [];
  const rollbackSet = new Set(rollbacks.map((version) => version.version));

  return (
    <HudSection kicker="Native profiles" title="Agent profiles" note="Effective profile policy, version history, drafts, activation, and rollback. A run keeps the profile versions captured when it was created.">
      {!loading && !canWrite ? (
        <p role="status" className="mb-4 border border-[hsl(var(--warn))] bg-[hsl(var(--warn-soft))] p-3 text-sm text-[hsl(var(--warn))]">
          Read-only access. Saving drafts, activation, and rollback need an admin credential.
        </p>
      ) : null}
      {listError ? <p role="alert" className="mb-4 text-sm text-[hsl(var(--danger))]">Could not load profiles: {listError}</p> : null}
      {!loading && entries.length === 0 && !listError ? <HudEmptyState>No agent profiles have been returned by the control plane.</HudEmptyState> : null}
      <div className="grid gap-4 lg:grid-cols-[0.8fr_1.6fr]">
        <nav aria-label="Agent profiles" className="grid content-start gap-2">
          {entries.map((entry) => {
            const label = entry.profile ? profileVersionLabel(entry.profile) : `draft v${entry.identity?.draft_version ?? entry.identity?.latest_version}`;
            return (
              <button
                aria-current={entry.id === selectedID ? 'true' : undefined}
                className={`hud-panel p-3 text-left ${entry.id === selectedID ? 'border-[hsl(var(--signal))]' : ''}`}
                key={entry.id}
                onClick={() => select(entry.id)}
                type="button"
              >
                <span className="block text-sm font-semibold text-[hsl(var(--ink))]">{entry.profile?.name ?? entry.id}</span>
                <span className="mt-1 block text-xs text-[hsl(var(--faint))]">
                  {entry.id} · {label}
                  {entry.identity?.draft_version ? ` · draft v${entry.identity.draft_version} pending` : ''}
                </span>
              </button>
            );
          })}
        </nav>

        <div>
          {detailError ? <p role="alert" className="text-sm text-[hsl(var(--danger))]">Could not load {selectedID}: {detailError}</p> : null}
          {feedback ? (
            <p role={feedback.tone === 'error' ? 'alert' : 'status'} className={`mb-3 border p-3 text-sm ${feedback.tone === 'error' ? 'border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] text-[hsl(var(--danger))]' : 'border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] text-[hsl(var(--confirm))]'}`}>
              {feedback.message}
            </p>
          ) : null}
          {detail ? (
            <HudPanelStrong className="p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <HudLabel className="text-[hsl(var(--faint))]">{detail.profile.role}</HudLabel>
                  <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{detail.profile.name}</h3>
                </div>
                {canWrite && !editing ? (
                  <button className={chipClass} onClick={() => setEditing({ initial: profileDraftFrom(draftVersion ?? detail.profile), expectedVersion: detail.identity.latest_version, key: Date.now() })} type="button">
                    Edit as new draft
                  </button>
                ) : null}
              </div>
              <div className="mt-4 grid gap-3 md:grid-cols-3">
                <HudField label="Serving" value={profileVersionLabel(detail.profile)} />
                <HudField label="Default model" value={detail.profile.default_model || 'none'} />
                <HudField label="Allowed models" value={modelList(detail.profile.allowed_models)} />
                <HudField label="Executors" value={arrayOrEmpty(detail.profile.allowed_executors).join(', ') || 'none'} />
                <HudField label="Versions" value={`active ${detail.identity.active_version || 'static'} · draft ${detail.identity.draft_version || 'none'} · latest ${detail.identity.latest_version}`} />
                <HudField label="Contract SHA-256" value={shortHash(detail.profile.prompt_contract_sha256)} />
              </div>
              <pre className="mt-4 overflow-auto whitespace-pre-wrap border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] p-3 text-xs text-[hsl(var(--ink))]">{detail.profile.prompt_contract}</pre>

              {editing ? (
                <ProfileDraftEditor
                  key={editing.key}
                  expectedVersion={editing.expectedVersion}
                  initial={editing.initial}
                  profileID={selectedID}
                  onCancel={() => setEditing(null)}
                  onForbidden={() => setAccess({ role: 'read', can_write: false })}
                  onReload={() => {
                    void loadDetail(selectedID).then((identity) => {
                      if (identity) setEditing((current) => (current ? { ...current, expectedVersion: identity.latest_version } : current));
                    });
                  }}
                  onSaved={(result) => void refreshAfterChange(`Saved draft v${result.version.version} of ${selectedID}. It does not serve runs until activated.`)}
                />
              ) : null}

              {(draftVersion || rollbacks.length > 0) && canWrite ? (
                <label className={`${labelClass} mt-4`}>
                  Reason for activation or rollback (audited)
                  <input className={fieldClass} disabled={busy} value={reason} onChange={(event) => setReason(event.target.value)} />
                </label>
              ) : null}
              {draftVersion ? (
                <div className="mt-3 flex flex-wrap items-center gap-2">
                  <span className="text-sm text-[hsl(var(--muted))]">Draft v{draftVersion.version} is saved and not serving.</span>
                  <button className={`${chipClass} border-[hsl(var(--confirm))] text-[hsl(var(--confirm))]`} disabled={!canWrite || busy} onClick={() => void activate(draftVersion.version)} type="button">
                    Activate v{draftVersion.version}
                  </button>
                </div>
              ) : null}

              <h4 className="hud-label mt-6 text-[hsl(var(--signal))]">Version history</h4>
              {detail.versions.length === 0 ? <HudEmptyState className="mt-2">Only the static default exists. Saving a draft creates version 1.</HudEmptyState> : null}
              <ol className="mt-2 grid gap-2">
                {detail.versions.map((version) => (
                  <VersionRow busy={busy} canWrite={canWrite} identity={detail.identity} key={version.version} onRollback={rollbackSet.has(version.version) && canWrite ? (value) => void rollback(value) : undefined} version={version} />
                ))}
              </ol>

              <h4 className="hud-label mt-6 text-[hsl(var(--signal))]">Audit</h4>
              {detail.audit.length === 0 ? <HudEmptyState className="mt-2">No audit entries.</HudEmptyState> : null}
              <ul className="mt-2 grid gap-1 text-xs text-[hsl(var(--muted))]">
                {detail.audit.map((entry) => (
                  <li key={entry.id}>
                    {formatTime(entry.created_at)} · {entry.action} v{entry.version} by {entry.actor}
                    {entry.previous_active_version ? ` (was v${entry.previous_active_version})` : ''}
                    {entry.reason ? ` · ${entry.reason}` : ''}
                  </li>
                ))}
              </ul>
            </HudPanelStrong>
          ) : null}
        </div>
      </div>
    </HudSection>
  );
}
