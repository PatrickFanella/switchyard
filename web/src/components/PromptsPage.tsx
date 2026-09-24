import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { arrayOrEmpty, messageFrom } from '../api';
import { confirmNavigation, registerNavigationGuard } from '../navigationGuard';
import {
  loadPromptInventory,
  loadWorkflowDefinition,
  modelConstraint,
  previewPrompt,
  previewRequest,
  promptKey,
  savePromptDraft,
  shortHash,
  stepTemplateText,
  templateVariables,
  type PromptContractEntry,
  type PromptInventory,
  type PromptPreview,
  type PromptProblem,
  type PromptTemplate,
} from '../promptAuthoring';
import { profileVersionLabel } from '../profileVersions';
import type { WorkflowDefinition } from '../types';
import { loadControlAccess, type ControlAccess } from '../workflowDrafts';
import { HudEmptyState, HudErrorState, HudLabel, HudLoadingState, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';
import { WorkflowVersionsPanel } from './WorkflowVersionsPanel';

const fieldClass = 'mt-1 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-1 font-mono text-sm text-[hsl(var(--ink))] disabled:opacity-70';
const chipClass = 'hud-chip px-3 py-1.5 text-[0.65rem] disabled:opacity-50';
const labelClass = 'block text-xs text-[hsl(var(--muted))]';

export const unsavedPromptMessage = 'You have unsaved changes to this prompt template. Leave and discard them?';

type Feedback = { tone: 'error' | 'ok'; message: string };

function ProblemList({ problems }: { problems: PromptProblem[] }) {
  if (problems.length === 0) return null;
  return (
    <ul className="grid gap-1 text-xs" data-testid="prompt-problems">
      {problems.map((problem) => (
        <li key={`${problem.severity}-${problem.message}`} className={problem.severity === 'error' ? 'text-[hsl(var(--danger))]' : 'text-[hsl(var(--warn))]'}>
          {problem.severity}: {problem.message}
        </li>
      ))}
    </ul>
  );
}

function ProfileConstraints({ template, contract }: { template: PromptTemplate; contract?: PromptContractEntry }) {
  if (!template.profile_id) {
    return <p className="text-xs text-[hsl(var(--faint))]">This step uses no agent profile, so no prompt contract is prepended.</p>;
  }
  if (!contract) {
    return <p className="text-xs text-[hsl(var(--danger))]">Profile {template.profile_id} is not an effective agent profile.</p>;
  }
  return (
    <HudPanel className="bg-[hsl(var(--panel-soft))] p-3" >
      <div data-testid="prompt-profile-constraints" className="grid gap-1 text-xs text-[hsl(var(--muted))]">
        <HudLabel className="text-[hsl(var(--signal))]">Profile constraints · {contract.profile_id}</HudLabel>
        <span>
          {contract.name ?? contract.profile_id} · {profileVersionLabel({ version: contract.version, source: contract.source, status: contract.status })} · contract sha256 {shortHash(contract.prompt_contract_sha256)}
        </span>
        <span>Models: {modelConstraint(contract)}{template.model ? ` · step model ${template.model}` : ''}</span>
        <span>Executors: {arrayOrEmpty(contract.allowed_executors).join(', ') || 'none'}</span>
        <details>
          <summary className="cursor-pointer">Prompt contract (prepended by the runner)</summary>
          <pre className="mt-1 whitespace-pre-wrap break-words text-[hsl(var(--ink))]">{contract.prompt_contract}</pre>
        </details>
        <span>
          The contract is owned by the profile and versioned there. <a className="underline" href="#/agents">Edit it on the Agents page</a>; do not copy it into this template.
        </span>
      </div>
    </HudPanel>
  );
}

function PreviewResult({ preview }: { preview: PromptPreview }) {
  return (
    <div className="grid gap-2" data-testid="prompt-preview">
      <p className={`text-sm ${preview.ok ? 'text-[hsl(var(--confirm))]' : 'text-[hsl(var(--danger))]'}`}>
        {preview.ok ? 'Preview rendered with every variable filled.' : 'Preview is incomplete; fix the items below.'} No model was called and nothing was run.
        {preview.redacted ? ' Credential-shaped values are redacted.' : ''}
      </p>
      {preview.missing_variables.length > 0 ? (
        <ul role="alert" className="grid gap-1 text-xs text-[hsl(var(--danger))]" data-testid="prompt-missing-variables">
          {preview.missing_variables.map((missing) => (
            <li key={missing.name}>{missing.message}</li>
          ))}
        </ul>
      ) : null}
      <ProblemList problems={preview.problems} />
      <HudLabel className="text-[hsl(var(--faint))]">Model prompt</HudLabel>
      <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] p-3 text-xs text-[hsl(var(--ink))]">{preview.rendered}</pre>
    </div>
  );
}

function PromptTemplateEditor({
  template,
  contract,
  readOnly,
  onChanged,
  onReadOnly,
}: {
  template: PromptTemplate;
  contract?: PromptContractEntry;
  readOnly: boolean;
  onChanged: (notice: string) => void;
  onReadOnly: () => void;
}) {
  const [definition, setDefinition] = useState<WorkflowDefinition | null>(null);
  const [loadError, setLoadError] = useState('');
  const [text, setText] = useState('');
  const [baseline, setBaseline] = useState('');
  const [samples, setSamples] = useState<Record<string, string>>({});
  const [preview, setPreview] = useState<PromptPreview | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [stale, setStale] = useState(false);
  const feedbackRef = useRef<HTMLDivElement>(null);
  const templateID = useId();
  const dirty = definition !== null && text !== baseline;
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;

  const load = useCallback(async () => {
    setLoadError('');
    try {
      const loaded = await loadWorkflowDefinition(template.workflow_id);
      const stored = stepTemplateText(loaded, template.step_id);
      if (stored === null) {
        setLoadError(`Step ${template.step_id} is no longer in ${template.workflow_id} version ${loaded.version ?? '?'}.`);
        return;
      }
      setDefinition(loaded);
      setText(stored);
      setBaseline(stored);
      setStale(false);
    } catch (error) {
      setLoadError(messageFrom(error));
    }
  }, [template.workflow_id, template.step_id]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const unregister = registerNavigationGuard(() => !dirtyRef.current || window.confirm(unsavedPromptMessage));
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

  const variables = useMemo(() => templateVariables(text), [text]);

  const runPreview = async () => {
    setPreviewing(true);
    const result = await previewPrompt(previewRequest(template, text, samples));
    setPreviewing(false);
    if (result.ok) {
      setPreview(result.preview);
      setFeedback(null);
      return;
    }
    setPreview(null);
    if (result.kind === 'forbidden') onReadOnly();
    setFeedback({ tone: 'error', message: result.message });
  };

  const save = async () => {
    if (!definition) return;
    setSaving(true);
    const result = await savePromptDraft(definition, template.step_id, text);
    setSaving(false);
    if (result.ok) {
      dirtyRef.current = false;
      setDefinition(result.definition);
      setBaseline(text);
      const active = result.definition.active_version ? `Version ${result.definition.active_version} still serves new runs` : 'No version serves runs yet';
      onChanged(`Saved ${template.workflow_id} draft version ${result.definition.version}. ${active}; activate the draft below to use it. Nothing was run.`);
      return;
    }
    if (result.kind === 'forbidden') onReadOnly();
    if (result.kind === 'conflict') setStale(true);
    setFeedback({ tone: 'error', message: result.message });
  };

  const revert = () => {
    setText(baseline);
    setPreview(null);
    setFeedback(null);
  };

  if (loadError) return <HudErrorState role="alert">Could not load the workflow definition: {loadError}</HudErrorState>;
  if (!definition) return <HudLoadingState>Loading {template.workflow_id}…</HudLoadingState>;

  const storedProblems = dirty ? [] : template.problems;
  return (
    <div className="grid gap-4" data-testid="prompt-editor">
      <div>
        <HudLabel className="text-[hsl(var(--faint))]">
          {template.workflow_id} · step {template.position} {template.step_id}
        </HudLabel>
        <h3 className="mt-1 text-lg font-semibold text-[hsl(var(--ink))]">{template.step_name}</h3>
        <p className="mt-1 text-xs text-[hsl(var(--muted))]">
          Owned by workflow definition {template.workflow_id}, input <code>{template.input}</code>. Editing latest version {definition.version}; active version {definition.active_version || 'none'}.
          {' '}Saving creates a draft version. It does not activate the draft, start a run, or publish anything.
        </p>
      </div>
      <ProfileConstraints template={template} contract={contract} />
      <label className={labelClass} htmlFor={templateID}>
        Template
      </label>
      <textarea
        id={templateID}
        aria-label="Template"
        className={`${fieldClass} min-h-40`}
        value={text}
        readOnly={readOnly}
        disabled={saving}
        onChange={(event) => {
          setText(event.target.value);
          setPreview(null);
        }}
      />
      <p className="text-xs text-[hsl(var(--faint))]">
        Use <code>{'{{name}}'}</code> for a run input and <code>{'{{steps.<step_id>.outputs.<name>}}'}</code> for an earlier step's output. The runner substitutes step outputs first, then run inputs, then prepends the profile contract.
      </p>
      <ProblemList problems={storedProblems} />
      <fieldset className="grid gap-2" disabled={readOnly}>
        <legend className="text-xs uppercase tracking-[0.14em] text-[hsl(var(--faint))]">Sample values</legend>
        {variables.inputs.length === 0 && variables.stepOutputs.length === 0 ? <p className="text-xs text-[hsl(var(--faint))]">The template reads no variables.</p> : null}
        {variables.inputs.map((name) => (
          <label key={`input-${name}`} className={labelClass}>
            Run input {name}
            <input className={fieldClass} value={samples[name] ?? ''} onChange={(event) => setSamples((current) => ({ ...current, [name]: event.target.value }))} />
          </label>
        ))}
        {variables.stepOutputs.map((output) => (
          <label key={`output-${output.key}`} className={labelClass}>
            Step output {output.key}
            <textarea className={fieldClass} value={samples[output.key] ?? ''} onChange={(event) => setSamples((current) => ({ ...current, [output.key]: event.target.value }))} />
          </label>
        ))}
      </fieldset>
      {readOnly ? (
        <p className="text-xs text-[hsl(var(--muted))]">Read-only access: an admin credential is required to preview, save, activate, or roll back prompts.</p>
      ) : (
        <div className="flex flex-wrap gap-2">
          <button type="button" className={chipClass} disabled={previewing || saving} onClick={() => void runPreview()}>
            {previewing ? 'Rendering…' : 'Preview'}
          </button>
          <button type="button" className={chipClass} disabled={!dirty || saving} onClick={() => void save()}>
            {saving ? 'Saving…' : 'Save draft'}
          </button>
          <button type="button" className={chipClass} disabled={!dirty || saving} onClick={revert}>
            Revert
          </button>
        </div>
      )}
      {feedback ? (
        <div ref={feedbackRef} tabIndex={-1} role={feedback.tone === 'error' ? 'alert' : 'status'} className={`text-sm ${feedback.tone === 'error' ? 'text-[hsl(var(--danger))]' : 'text-[hsl(var(--confirm))]'}`}>
          {feedback.message}
          {stale ? (
            <button type="button" className={`${chipClass} ml-2`} onClick={() => void load()}>
              Reload latest version
            </button>
          ) : null}
        </div>
      ) : null}
      {preview ? <PreviewResult preview={preview} /> : null}
      <div>
        <HudLabel className="text-[hsl(var(--signal))]">Versions and promotion</HudLabel>
        <p className="mt-1 text-xs text-[hsl(var(--muted))]">A template is promoted by activating its workflow-definition version, and rolled back the same way. Runs already created keep the version they captured.</p>
        <WorkflowVersionsPanel
          key={`${definition.version ?? 0}-${definition.active_version ?? 0}`}
          definition={definition}
          readOnly={readOnly}
          onChanged={(notice) => {
            onChanged(notice);
            void load();
          }}
          onReadOnly={onReadOnly}
        />
      </div>
    </div>
  );
}

function templateStatus(template: PromptTemplate) {
  if (!template.active_version) return { label: 'draft only', tone: 'queued' };
  if (template.changed_since_active) return { label: `draft v${template.version} differs from active v${template.active_version}`, tone: 'queued' };
  return { label: `active v${template.active_version}`, tone: 'succeeded' };
}

/** Prompt authoring over the two prompt owners: workflow templates and profile contracts. */
export function PromptsPage() {
  const [inventory, setInventory] = useState<PromptInventory | null>(null);
  const [error, setError] = useState('');
  const [access, setAccess] = useState<ControlAccess | null>(null);
  const [forcedReadOnly, setForcedReadOnly] = useState(false);
  const [selected, setSelected] = useState('');
  const [notice, setNotice] = useState('');

  const reload = useCallback(async () => {
    try {
      const [loaded, role] = await Promise.all([loadPromptInventory(), loadControlAccess()]);
      setInventory(loaded);
      setAccess(role);
      setError('');
    } catch (loadError) {
      setError(messageFrom(loadError));
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  const readOnly = forcedReadOnly || (access !== null && !access.can_write);
  const contracts = useMemo(() => new Map((inventory?.contracts ?? []).map((contract) => [contract.profile_id, contract])), [inventory]);
  const current = inventory?.templates.find((template) => promptKey(template) === selected);

  const select = (key: string) => {
    if (key === selected || !confirmNavigation()) return;
    setNotice('');
    setSelected(key);
  };

  return (
    <HudSection kicker="Workflow policy" title="Prompt authoring" note="Edit model-step templates as workflow drafts, preview them with sample values, and promote them through workflow versions. Profile prompt contracts are edited on the Agents page.">
      <HudPanel className="mb-4 p-3">
        <HudLabel className="text-[hsl(var(--signal))]">Who owns each prompt</HudLabel>
        <ul className="mt-2 grid gap-1 text-xs text-[hsl(var(--muted))]">
          <li>Step templates: the <code>prompt</code> or <code>template</code> input of a model step, versioned with its workflow definition.</li>
          <li>Prompt contracts: the <code>prompt_contract</code> of an agent profile, versioned with the profile and prepended by the runner.</li>
          <li>Built-in prompts: assembled in code by the Devlog and Open Pilot executors; they change only with a code change.</li>
        </ul>
      </HudPanel>
      {readOnly ? (
        <p className="mb-4 text-sm text-[hsl(var(--muted))]" data-testid="prompts-read-only">
          Read-only access. You can inspect templates, contracts, and versions; editing, previewing, activation, and rollback need an admin credential.
        </p>
      ) : null}
      {notice ? (
        <p role="status" className="mb-4 text-sm text-[hsl(var(--confirm))]">
          {notice}
        </p>
      ) : null}
      {error ? <HudErrorState role="alert">Could not load prompts: {error}</HudErrorState> : null}
      {!inventory && !error ? <HudLoadingState>Loading prompts…</HudLoadingState> : null}
      {inventory ? (
        <div className="grid gap-4 lg:grid-cols-[minmax(0,20rem)_1fr]">
          <div className="grid content-start gap-4">
            <HudPanelStrong className="p-3">
              <HudLabel className="text-[hsl(var(--signal))]">Step templates</HudLabel>
              {inventory.templates.length === 0 ? <HudEmptyState className="mt-2">No workflow definition has a model step yet.</HudEmptyState> : null}
              <ul className="mt-2 grid gap-2">
                {inventory.templates.map((template) => {
                  const status = templateStatus(template);
                  const key = promptKey(template);
                  const errors = template.problems.filter((problem) => problem.severity === 'error').length;
                  return (
                    <li key={key}>
                      <button type="button" aria-pressed={key === selected} aria-label={`Open ${key}`} className={`w-full border p-2 text-left text-sm ${key === selected ? 'border-[hsl(var(--signal))]' : 'border-[hsl(var(--border-faint))]'}`} onClick={() => select(key)}>
                        <span className="block text-[hsl(var(--ink))]">{template.workflow_name || template.workflow_id} · {template.step_id}</span>
                        <span className={`hud-chip mt-1 inline-block px-2 py-0.5 text-[0.6rem] ${statusClasses(status.tone)}`}>{status.label}</span>
                        <span className="mt-1 block text-xs text-[hsl(var(--faint))]">
                          {template.profile_id ? `profile ${template.profile_id}` : 'no profile'} · {template.variables.length} variables{errors ? ` · ${errors} errors` : ''}
                        </span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            </HudPanelStrong>
            <HudPanelStrong className="p-3">
              <HudLabel className="text-[hsl(var(--signal))]">Profile prompt contracts</HudLabel>
              <ul className="mt-2 grid gap-2 text-xs" data-testid="prompt-contracts">
                {inventory.contracts.map((contract) => (
                  <li key={contract.profile_id} className="text-[hsl(var(--muted))]">
                    <span className="text-[hsl(var(--ink))]">{contract.profile_id}</span> · {profileVersionLabel({ version: contract.version, source: contract.source, status: contract.status })} · used by {contract.referenced_by.length} steps
                  </li>
                ))}
              </ul>
              <a className="mt-2 inline-block text-xs underline" href="#/agents">
                Edit contracts on the Agents page
              </a>
            </HudPanelStrong>
            <HudPanelStrong className="p-3">
              <HudLabel className="text-[hsl(var(--signal))]">Built-in prompts</HudLabel>
              {inventory.built_in.length === 0 ? <p className="mt-2 text-xs text-[hsl(var(--faint))]">None.</p> : null}
              <ul className="mt-2 grid gap-2 text-xs" data-testid="prompt-built-in">
                {inventory.built_in.map((step) => (
                  <li key={`${step.workflow_id}/${step.step_id}`} className="text-[hsl(var(--muted))]">
                    <span className="text-[hsl(var(--ink))]">{step.workflow_id} · {step.step_id}</span>
                    {step.profile_id ? ` · profile ${step.profile_id}` : ''}
                    <span className="block text-[hsl(var(--faint))]">{step.note}</span>
                  </li>
                ))}
              </ul>
            </HudPanelStrong>
          </div>
          <HudPanelStrong className="p-4">
            {current ? (
              <PromptTemplateEditor
                key={promptKey(current)}
                template={current}
                contract={current.profile_id ? contracts.get(current.profile_id) : undefined}
                readOnly={readOnly}
                onChanged={(message) => {
                  setNotice(message);
                  void reload();
                }}
                onReadOnly={() => setForcedReadOnly(true)}
              />
            ) : (
              <HudEmptyState>Select a step template to inspect, preview, or edit it.</HudEmptyState>
            )}
          </HudPanelStrong>
        </div>
      ) : null}
    </HudSection>
  );
}
