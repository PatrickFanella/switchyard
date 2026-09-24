import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from 'react';
import { registerNavigationGuard, unsavedChangesMessage } from '../navigationGuard';
import type { AgentProfile, WorkflowDefinition } from '../types';
import {
  additionalStepInputs,
  definitionExecutors,
  draftFingerprint,
  emptyInputField,
  emptyOutput,
  emptyStep,
  executorSettings,
  inputFieldTypes,
  isDefinitionExecutor,
  nextDraftKey,
  outputTypes,
  profileAllowsModel,
  profilesForExecutor,
  saveWorkflowDraft,
  stepInput,
  stepOutputOptions,
  stepReferenceProblems,
  validateDraftForSave,
  withStepInput,
  type DraftInputField,
  type DraftOutput,
  type DraftStep,
  type WorkflowDraft,
} from '../workflowDrafts';
import { HudErrorState, HudLabel, HudPanel, HudPanelStrong } from './hud';

const fieldClass = 'mt-1 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-1 text-sm text-[hsl(var(--ink))] disabled:opacity-70';
const chipClass = 'hud-chip px-3 py-1.5 text-[0.65rem] disabled:opacity-50';
const labelClass = 'block text-xs text-[hsl(var(--muted))]';

export type WorkflowDraftEditorProps = {
  mode: 'create' | 'edit';
  initialDraft: WorkflowDraft;
  /** The stored definition being edited; its status and version gate the save. */
  baseDefinition?: WorkflowDefinition;
  profiles: AgentProfile[];
  existingIDs: string[];
  readOnly: boolean;
  onSaved: (definition: WorkflowDefinition) => void;
  onCancel: () => void;
  onReadOnly: () => void;
  onReloadLatest?: () => void;
};

type SaveFeedback = { kind: 'client' | 'validation' | 'forbidden' | 'conflict' | 'error'; messages: string[] } | null;

function TextField({ label, value, onChange, disabled, multiline = false, hint, required = false, placeholder }: { label: string; value: string; onChange: (value: string) => void; disabled: boolean; multiline?: boolean; hint?: string; required?: boolean; placeholder?: string }) {
  return (
    <label className={labelClass}>
      {label}
      {required ? <span aria-hidden="true"> *</span> : null}
      {multiline ? (
        <textarea className={`${fieldClass} min-h-20 font-mono`} value={value} disabled={disabled} required={required} placeholder={placeholder} onChange={(event) => onChange(event.target.value)} />
      ) : (
        <input className={fieldClass} value={value} disabled={disabled} required={required} placeholder={placeholder} onChange={(event) => onChange(event.target.value)} />
      )}
      {hint ? <span className="mt-1 block text-[0.7rem] text-[hsl(var(--faint))]">{hint}</span> : null}
    </label>
  );
}

function SelectField({ label, value, onChange, disabled, children }: { label: string; value: string; onChange: (value: string) => void; disabled: boolean; children: ReactNode }) {
  return (
    <label className={labelClass}>
      {label}
      <select className={fieldClass} value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)}>
        {children}
      </select>
    </label>
  );
}

function CheckboxField({ label, checked, onChange, disabled }: { label: string; checked: boolean; onChange: (checked: boolean) => void; disabled: boolean }) {
  return (
    <label className="flex items-center gap-2 text-xs text-[hsl(var(--muted))]">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} />
      {label}
    </label>
  );
}

function InputFieldRow({ field, index, disabled, onChange, onRemove }: { field: DraftInputField; index: number; disabled: boolean; onChange: (field: DraftInputField) => void; onRemove: () => void }) {
  return (
    <li className="grid gap-2 border-b border-[hsl(var(--border-faint))] pb-3 md:grid-cols-[1fr_9rem_2fr_auto_auto] md:items-end" data-input-field={index}>
      <TextField label="Field name" value={field.name} disabled={disabled} onChange={(name) => onChange({ ...field, name })} />
      <SelectField label="Type" value={field.type} disabled={disabled} onChange={(type) => onChange({ ...field, type })}>
        {!(inputFieldTypes as readonly string[]).includes(field.type) ? <option value={field.type}>{field.type || '(unset)'}</option> : null}
        {inputFieldTypes.map((type) => <option key={type} value={type}>{type}</option>)}
      </SelectField>
      <TextField label="Description" value={field.description} disabled={disabled} onChange={(description) => onChange({ ...field, description })} />
      <CheckboxField label="Required" checked={field.required} disabled={disabled} onChange={(required) => onChange({ ...field, required })} />
      <button type="button" className={chipClass} disabled={disabled} onClick={onRemove} aria-label={`Remove input field ${field.name || index + 1}`}>Remove</button>
    </li>
  );
}

function OutputRow({ output, stepLabel, disabled, onChange, onRemove }: { output: DraftOutput; stepLabel: string; disabled: boolean; onChange: (output: DraftOutput) => void; onRemove: () => void }) {
  const known = (outputTypes as readonly string[]).includes(output.type);
  return (
    <li className="grid gap-2 border-b border-[hsl(var(--border-faint))] pb-3" data-output={output.name}>
      <div className="grid gap-2 md:grid-cols-[1fr_9rem_2fr_auto_auto] md:items-end">
        <TextField label="Output name" value={output.name} disabled={disabled} onChange={(name) => onChange({ ...output, name })} hint="Lowercase letters, digits and _; starts with a letter." />
        <SelectField label="Output type" value={output.type} disabled={disabled} onChange={(type) => onChange({ ...output, type })}>
          {!known ? <option value={output.type}>{output.type || '(unset)'}</option> : null}
          {outputTypes.map((type) => <option key={type} value={type}>{type}</option>)}
        </SelectField>
        <TextField label="Output description" value={output.description} disabled={disabled} onChange={(description) => onChange({ ...output, description })} />
        <CheckboxField label="Required output" checked={output.required} disabled={disabled} onChange={(required) => onChange({ ...output, required })} />
        <button type="button" className={chipClass} disabled={disabled} onClick={onRemove} aria-label={`Remove output ${output.name || '(unnamed)'} from ${stepLabel}`}>Remove</button>
      </div>
      <div className="grid gap-2 md:grid-cols-3">
        {output.type === 'string' ? (
          <>
            <TextField label="maxLength" value={output.maxLength} disabled={disabled} onChange={(maxLength) => onChange({ ...output, maxLength })} hint="Maximum length in characters." />
            <TextField label="Allowed values (enum)" value={output.enumValues} disabled={disabled} onChange={(enumValues) => onChange({ ...output, enumValues })} hint="Comma-separated." />
          </>
        ) : null}
        {output.type === 'array' ? <TextField label="maxItems" value={output.maxItems} disabled={disabled} onChange={(maxItems) => onChange({ ...output, maxItems })} /> : null}
        {output.type === 'object' || output.type === 'array' || output.keywords.trim() ? (
          <TextField
            label="Nested keywords (JSON)"
            value={output.keywords}
            disabled={disabled}
            multiline
            onChange={(keywords) => onChange({ ...output, keywords })}
            hint={output.type === 'array' ? 'For example {"items": {"type": "string"}}.' : 'For example {"properties": {"id": {"type": "integer"}}, "required": ["id"]}.'}
          />
        ) : null}
      </div>
    </li>
  );
}

function OutputBindingPicker({ draft, stepIndex, step, disabled, onInsert }: { draft: WorkflowDraft; stepIndex: number; step: DraftStep; disabled: boolean; onInsert: (inputName: string, reference: string) => void }) {
  const options = useMemo(() => stepOutputOptions(draft, stepIndex), [draft, stepIndex]);
  const targets = useMemo(() => {
    const settings = isDefinitionExecutor(step.executor) ? executorSettings[step.executor].filter((setting) => !setting.checkbox).map((setting) => setting.name) : [];
    const extras = additionalStepInputs(step).map((input) => input.name.trim()).filter(Boolean);
    return [...new Set([...settings, ...extras])];
  }, [step]);
  const [reference, setReference] = useState('');
  const [target, setTarget] = useState('');
  const selectedReference = options.some((option) => option.reference === reference) ? reference : options[0]?.reference ?? '';
  const selectedTarget = targets.includes(target) ? target : targets[0] ?? '';

  if (stepIndex === 0) {
    return <p className="text-xs text-[hsl(var(--faint))]">The first step has no earlier outputs to bind. Later steps can bind this step's outputs.</p>;
  }
  if (options.length === 0) {
    return <p className="text-xs text-[hsl(var(--faint))]">Earlier steps need an ID before their outputs can be bound.</p>;
  }
  const sourceLabel = { declared: 'declared', executor: 'published by executor', 'internal-input': 'internal output input' } as const;
  return (
    <fieldset className="grid gap-2 md:grid-cols-[2fr_1fr_auto] md:items-end" aria-label={`Bind an earlier step output into step ${step.id || stepIndex + 1}`}>
      <SelectField label="Earlier step output" value={selectedReference} disabled={disabled} onChange={setReference}>
        {options.map((option) => (
          <option key={option.reference} value={option.reference}>
            {`${option.stepPosition}. ${option.stepID} → ${option.name} (${sourceLabel[option.source]}${option.type ? `, ${option.type}` : ''})`}
          </option>
        ))}
      </SelectField>
      <SelectField label="Insert into input" value={selectedTarget} disabled={disabled || targets.length === 0} onChange={setTarget}>
        {targets.map((name) => <option key={name} value={name}>{name}</option>)}
      </SelectField>
      <button type="button" className={chipClass} disabled={disabled || !selectedReference || !selectedTarget} onClick={() => onInsert(selectedTarget, selectedReference)}>Insert reference</button>
      <p className="text-[0.7rem] text-[hsl(var(--faint))] md:col-span-3">
        Inserts <code>{selectedReference}</code>. Declared outputs come from the earlier step's output shape; executor outputs are published on success (see docs/reference/step-outputs.md).
      </p>
    </fieldset>
  );
}

function StepEditor({
  draft,
  step,
  index,
  count,
  profiles,
  disabled,
  onChange,
  onMove,
  onRemove,
}: {
  draft: WorkflowDraft;
  step: DraftStep;
  index: number;
  count: number;
  profiles: AgentProfile[];
  disabled: boolean;
  onChange: (step: DraftStep) => void;
  onMove: (offset: -1 | 1) => void;
  onRemove: () => void;
}) {
  const stepLabel = `step ${step.id || index + 1}`;
  const settings = isDefinitionExecutor(step.executor) ? executorSettings[step.executor] : [];
  const extras = additionalStepInputs(step);
  const allowedProfiles = profilesForExecutor(profiles, step.executor);
  const selectedProfile = profiles.find((profile) => profile.id === step.profile);
  const profileAllowed = step.profile === '' || allowedProfiles.some((profile) => profile.id === step.profile);
  const problems = stepReferenceProblems(draft, index);

  const updateExtra = (key: string, patch: Partial<{ name: string; value: string }>) => onChange({ ...step, inputs: step.inputs.map((input) => (input.key === key ? { ...input, ...patch } : input)) });

  return (
    <li data-step-index={index}>
      <HudPanel className="grid gap-3 bg-[hsl(var(--panel-soft))] p-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h4 className="text-sm font-semibold text-[hsl(var(--ink))]">Step {index + 1}{step.id ? ` · ${step.id}` : ''}</h4>
          <div className="flex flex-wrap gap-1.5">
            <button type="button" className={chipClass} disabled={disabled || index === 0} onClick={() => onMove(-1)} aria-label={`Move ${stepLabel} up`}>Move up</button>
            <button type="button" className={chipClass} disabled={disabled || index === count - 1} onClick={() => onMove(1)} aria-label={`Move ${stepLabel} down`}>Move down</button>
            <button type="button" className={chipClass} disabled={disabled || count === 1} onClick={onRemove} aria-label={`Remove ${stepLabel}`}>Remove step</button>
          </div>
        </div>
        <div className="grid gap-2 md:grid-cols-3">
          <TextField label="Step ID" value={step.id} disabled={disabled} required onChange={(id) => onChange({ ...step, id })} hint="Referenced as {{steps.<id>.outputs.<name>}}." />
          <TextField label="Step name" value={step.name} disabled={disabled} required onChange={(name) => onChange({ ...step, name })} />
          <SelectField label="Executor" value={step.executor} disabled={disabled} onChange={(executor) => onChange({ ...step, executor })}>
            {!isDefinitionExecutor(step.executor) ? <option value={step.executor}>{step.executor} (not supported)</option> : null}
            {definitionExecutors.map((executor) => <option key={executor} value={executor}>{executor}</option>)}
          </SelectField>
        </div>
        <div className="grid gap-2 md:grid-cols-2">
          <SelectField label="Agent profile" value={step.profile} disabled={disabled} onChange={(profile) => onChange({ ...step, profile })}>
            <option value="">No profile</option>
            {allowedProfiles.map((profile) => <option key={profile.id} value={profile.id}>{`${profile.name} (${profile.id})`}</option>)}
            {!profileAllowed ? <option value={step.profile}>{`${step.profile} (does not allow ${step.executor})`}</option> : null}
          </SelectField>
          {step.executor === 'model' || step.model ? (
            <TextField
              label="Model"
              value={step.model}
              disabled={disabled}
              onChange={(model) => onChange({ ...step, model })}
              placeholder="${model}"
              hint={selectedProfile?.allowed_models?.length ? `Profile allows: ${selectedProfile.allowed_models.join(', ')} or \${model}.` : '${model} uses the run or profile default.'}
            />
          ) : null}
        </div>
        <p className="text-[0.7rem] text-[hsl(var(--faint))]">Only profiles that allow the {step.executor} executor are listed. The server checks the profile and model again on save.</p>
        {!profileAllowed ? <p className="text-xs text-[hsl(var(--warn))]" role="status">Profile {step.profile} does not allow the {step.executor} executor; the server will reject this draft.</p> : null}
        {selectedProfile && !profileAllowsModel(selectedProfile, step.model) ? <p className="text-xs text-[hsl(var(--warn))]" role="status">Profile {selectedProfile.id} does not allow model {step.model}; the server will reject this draft.</p> : null}

        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Executor settings</HudLabel>
          <div className="mt-2 grid gap-2 md:grid-cols-2">
            {settings.map((setting) =>
              setting.checkbox ? (
                <CheckboxField key={setting.name} label={setting.label} checked={stepInput(step, setting.name).toLowerCase() === 'true'} disabled={disabled} onChange={(checked) => onChange(withStepInput(step, setting.name, checked ? 'true' : ''))} />
              ) : (
                <TextField key={setting.name} label={setting.label} value={stepInput(step, setting.name)} disabled={disabled} multiline={setting.multiline} hint={setting.hint} onChange={(value) => onChange(withStepInput(step, setting.name, value))} />
              ),
            )}
            {!isDefinitionExecutor(step.executor) ? <p className="text-xs text-[hsl(var(--warn))]">Executor {step.executor} is not supported in workflow definitions. Choose internal, model, command, or http.</p> : null}
          </div>
        </div>

        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Additional inputs</HudLabel>
          {step.executor === 'internal' ? <p className="mt-1 text-[0.7rem] text-[hsl(var(--faint))]">An input named output.&lt;name&gt; publishes a named output from an internal step.</p> : null}
          <ul className="mt-2 grid gap-2">
            {extras.map((input) => (
              <li key={input.key} className="grid gap-2 md:grid-cols-[1fr_2fr_auto] md:items-end">
                <TextField label="Input name" value={input.name} disabled={disabled} onChange={(name) => updateExtra(input.key, { name })} />
                <TextField label="Input value" value={input.value} disabled={disabled} multiline onChange={(value) => updateExtra(input.key, { value })} />
                <button type="button" className={chipClass} disabled={disabled} onClick={() => onChange({ ...step, inputs: step.inputs.filter((item) => item.key !== input.key) })} aria-label={`Remove input ${input.name || '(unnamed)'} from ${stepLabel}`}>Remove</button>
              </li>
            ))}
          </ul>
          <button type="button" className={`${chipClass} mt-2`} disabled={disabled} onClick={() => onChange({ ...step, inputs: [...step.inputs, { key: nextDraftKey('input'), name: '', value: '' }] })} aria-label={`Add input to ${stepLabel}`}>Add input</button>
        </div>

        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Step output bindings</HudLabel>
          <div className="mt-2">
            <OutputBindingPicker draft={draft} stepIndex={index} step={step} disabled={disabled} onInsert={(inputName, reference) => onChange(withStepInput(step, inputName, `${stepInput(step, inputName)}${reference}`))} />
          </div>
          {problems.length > 0 ? (
            <ul className="mt-2 grid gap-1 text-xs text-[hsl(var(--warn))]" aria-label={`Reference problems in ${stepLabel}`}>
              {problems.map((problem) => <li key={problem}>{problem}</li>)}
            </ul>
          ) : null}
        </div>

        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Output contract (output_shape v1)</HudLabel>
          <p className="mt-1 text-[0.7rem] text-[hsl(var(--faint))]">Declared outputs are validated on every run. Supported keywords: type, description, required, maxLength and enum (string), maxItems and items (array), properties and required (object).</p>
          {step.outputs.length > 0 ? (
            <div className="mt-2">
              <TextField label="Output shape description" value={step.outputDescription} disabled={disabled} onChange={(outputDescription) => onChange({ ...step, outputDescription })} />
            </div>
          ) : null}
          <ul className="mt-2 grid gap-2">
            {step.outputs.map((output) => (
              <OutputRow
                key={output.key}
                output={output}
                stepLabel={stepLabel}
                disabled={disabled}
                onChange={(next) => onChange({ ...step, outputs: step.outputs.map((item) => (item.key === output.key ? next : item)) })}
                onRemove={() => onChange({ ...step, outputs: step.outputs.filter((item) => item.key !== output.key) })}
              />
            ))}
          </ul>
          <button type="button" className={`${chipClass} mt-2`} disabled={disabled} onClick={() => onChange({ ...step, outputs: [...step.outputs, emptyOutput()] })} aria-label={`Declare output for ${stepLabel}`}>Declare output</button>
        </div>
      </HudPanel>
    </li>
  );
}

export function WorkflowDraftEditor({ mode, initialDraft, baseDefinition, profiles, existingIDs, readOnly, onSaved, onCancel, onReadOnly, onReloadLatest }: WorkflowDraftEditorProps) {
  const [draft, setDraft] = useState<WorkflowDraft>(initialDraft);
  const [baseline, setBaseline] = useState(() => draftFingerprint(initialDraft));
  const [feedback, setFeedback] = useState<SaveFeedback>(null);
  const [saving, setSaving] = useState(false);
  const feedbackRef = useRef<HTMLDivElement>(null);
  const dirty = draftFingerprint(draft) !== baseline;
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;
  const disabled = readOnly || saving;
  const activeVersion = mode === 'edit' ? baseDefinition?.active_version ?? (baseDefinition?.status === 'active' ? baseDefinition.version : undefined) : undefined;

  useEffect(() => {
    const unregister = registerNavigationGuard(() => !dirtyRef.current || window.confirm(unsavedChangesMessage));
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

  const update = useCallback((patch: Partial<WorkflowDraft>) => setDraft((current) => ({ ...current, ...patch })), []);
  const updateStep = (index: number, step: DraftStep) => setDraft((current) => ({ ...current, steps: current.steps.map((item, itemIndex) => (itemIndex === index ? step : item)) }));
  const moveStep = (index: number, offset: -1 | 1) =>
    setDraft((current) => {
      const target = index + offset;
      if (target < 0 || target >= current.steps.length) return current;
      const steps = current.steps.slice();
      [steps[index], steps[target]] = [steps[target], steps[index]];
      return { ...current, steps };
    });

  const save = async () => {
    if (readOnly || saving) return;
    const { definition, errors } = validateDraftForSave(draft, { creating: mode === 'create', existingIDs });
    if (errors.length > 0) {
      setFeedback({ kind: 'client', messages: errors });
      return;
    }
    setSaving(true);
    setFeedback(null);
    try {
      const result = await saveWorkflowDraft(definition, mode === 'create' ? { creating: true } : { creating: false, baseVersion: baseDefinition?.version });
      if (result.ok) {
        dirtyRef.current = false;
        setBaseline(draftFingerprint(draft));
        onSaved(result.definition);
        return;
      }
      if (result.kind === 'forbidden') onReadOnly();
      setFeedback({ kind: result.kind, messages: [result.message] });
    } catch (error) {
      setFeedback({ kind: 'error', messages: [error instanceof Error ? error.message : 'Saving the draft failed.'] });
    } finally {
      setSaving(false);
    }
  };

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    void save();
  };

  const onKeyDown = (event: KeyboardEvent<HTMLFormElement>) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
      event.preventDefault();
      void save();
    } else if (event.key === 'Escape') {
      event.preventDefault();
      onCancel();
    }
  };

  const feedbackTitle = feedback?.kind === 'validation' ? 'The server rejected this draft' : feedback?.kind === 'client' ? 'Fix these problems before saving' : feedback?.kind === 'forbidden' ? 'Read-only access' : feedback?.kind === 'conflict' ? 'Save conflict' : 'Save failed';

  return (
    <form className="grid gap-4" onSubmit={onSubmit} onKeyDown={onKeyDown} aria-label={mode === 'create' ? 'New workflow draft' : `Edit workflow draft ${initialDraft.id}`} noValidate>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-[hsl(var(--muted))]">
          {mode === 'create' ? 'New draft' : `Editing ${initialDraft.id}${baseDefinition?.version ? ` · based on version ${baseDefinition.version}` : ''}`}
          {dirty ? ' · unsaved changes' : ' · no unsaved changes'}
        </p>
        <p className="text-[0.7rem] text-[hsl(var(--faint))]">Ctrl+S or ⌘S saves the draft; Escape leaves the editor.</p>
      </div>

      {readOnly ? (
        <HudErrorState role="status" data-read-only="true">
          <strong>Read-only access</strong>
          <span>This credential can view workflow definitions but cannot save drafts. Use an admin credential to edit.</span>
        </HudErrorState>
      ) : null}

      {feedback ? (
        <div ref={feedbackRef} tabIndex={-1} data-save-feedback={feedback.kind}>
          <HudErrorState role="alert">
            <strong>{feedbackTitle}</strong>
            <ul className="grid gap-1">
              {feedback.messages.map((message) => <li key={message} className="break-words">{message}</li>)}
            </ul>
            {feedback.kind === 'conflict' && onReloadLatest ? (
              <button type="button" className={`${chipClass} mt-2 self-start`} onClick={onReloadLatest}>Reload latest version (discard my edits)</button>
            ) : null}
          </HudErrorState>
        </div>
      ) : null}

      <HudPanelStrong className="grid gap-3 p-4">
        <h3 className="hud-label text-[hsl(var(--signal))]">Metadata</h3>
        <div className="grid gap-2 md:grid-cols-2">
          <TextField label="Workflow ID" value={draft.id} disabled={disabled || mode === 'edit'} required onChange={(id) => update({ id })} hint={mode === 'edit' ? 'The ID of a saved workflow cannot change. Duplicate it to use another ID.' : 'Letters, digits, ".", "_" and "-".'} />
          <TextField label="Workflow name" value={draft.name} disabled={disabled} required onChange={(name) => update({ name })} />
        </div>
        <TextField label="Description" value={draft.description} disabled={disabled} multiline onChange={(description) => update({ description })} />
        <div className="grid gap-2 md:grid-cols-2">
          <TextField label="Owns" value={draft.owns} disabled={disabled} onChange={(owns) => update({ owns })} hint="Comma-separated." />
          <TextField label="Uses" value={draft.uses} disabled={disabled} onChange={(uses) => update({ uses })} hint="Comma-separated." />
        </div>
      </HudPanelStrong>

      <HudPanelStrong className="grid gap-3 p-4">
        <h3 className="hud-label text-[hsl(var(--signal))]">Run input fields</h3>
        <p className="text-xs text-[hsl(var(--muted))]">Inputs a run supplies. Steps read them with {'{{name}}'} templates.</p>
        {draft.inputFields.length === 0 ? <p className="text-xs text-[hsl(var(--faint))]">No input fields declared.</p> : null}
        <ul className="grid gap-2">
          {draft.inputFields.map((field, index) => (
            <InputFieldRow
              key={field.key}
              field={field}
              index={index}
              disabled={disabled}
              onChange={(next) => update({ inputFields: draft.inputFields.map((item) => (item.key === field.key ? next : item)) })}
              onRemove={() => update({ inputFields: draft.inputFields.filter((item) => item.key !== field.key) })}
            />
          ))}
        </ul>
        <button type="button" className={`${chipClass} self-start`} disabled={disabled} onClick={() => update({ inputFields: [...draft.inputFields, emptyInputField()] })}>Add input field</button>
      </HudPanelStrong>

      <HudPanelStrong className="grid gap-3 p-4">
        <h3 className="hud-label text-[hsl(var(--signal))]">Steps (run in this order)</h3>
        <ol className="grid gap-3">
          {draft.steps.map((step, index) => (
            <StepEditor
              key={step.key}
              draft={draft}
              step={step}
              index={index}
              count={draft.steps.length}
              profiles={profiles}
              disabled={disabled}
              onChange={(next) => updateStep(index, next)}
              onMove={(offset) => moveStep(index, offset)}
              onRemove={() => setDraft((current) => ({ ...current, steps: current.steps.filter((item) => item.key !== step.key) }))}
            />
          ))}
        </ol>
        <button type="button" className={`${chipClass} self-start`} disabled={disabled} onClick={() => update({ steps: [...draft.steps, emptyStep('internal', `step-${draft.steps.length + 1}`)] })}>Add step</button>
      </HudPanelStrong>

      <HudPanelStrong className="grid gap-3 p-4">
        {activeVersion ? (
          <p className="text-xs text-[hsl(var(--muted))]" role="note">
            Version {activeVersion} of {initialDraft.id} is active. It keeps serving new runs after you save; activate the new draft from the version history when it is ready.
          </p>
        ) : null}
        <p className="text-xs text-[hsl(var(--muted))]">Saving writes a new draft version. It does not activate the workflow.</p>
        <div className="flex flex-wrap gap-2">
          <button type="submit" className={chipClass} disabled={disabled}>{saving ? 'Saving…' : 'Save draft'}</button>
          <button type="button" className={chipClass} onClick={onCancel}>{mode === 'create' ? 'Discard' : 'Cancel'}</button>
        </div>
      </HudPanelStrong>
    </form>
  );
}
