import { useEffect, useState, type ReactNode } from 'react';
import { ApiError, type ApiClient } from '../api';
import type { RuntimeConfig } from '../types';
import { Button, Input, NoticeBanner, Panel, PanelHeader, SectionIntro } from './ui';

type ConfigEditorProps = {
  api: ApiClient | null;
  visible: boolean;
  onUnauthorized: () => void;
};

export function ConfigEditor({ api, visible, onUnauthorized }: ConfigEditorProps) {
  const [original, setOriginal] = useState<RuntimeConfig | null>(null);
  const [draft, setDraft] = useState<RuntimeConfig | null>(null);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    if (!visible || !api) return;

    let cancelled = false;
    setLoading(true);
    setError(null);

    void api
      .readRuntimeConfig()
      .then((config) => {
        if (cancelled) return;
        setOriginal(config);
        setDraft(config);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 401) {
          onUnauthorized();
          return;
        }
        setError(err instanceof Error ? err.message : 'Failed to load config');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [api, onUnauthorized, visible]);

  const dirty = Boolean(original && draft && JSON.stringify(original) !== JSON.stringify(draft));

  function updateGeneration(patch: Partial<RuntimeConfig['generation']>) {
    setDraft((current) => (current ? { ...current, generation: { ...current.generation, ...patch } } : current));
  }

  async function handleSave() {
    if (!api || !draft) return;

    setSaving(true);
    setError(null);
    setNotice(null);

    try {
      const next = await api.saveRuntimeConfig(draft);
      setOriginal(next);
      setDraft(next);
      setNotice('Runtime config saved.');
    } catch (err: unknown) {
      if (err instanceof ApiError && err.status === 401) {
        onUnauthorized();
        return;
      }
      setError(err instanceof Error ? err.message : 'Failed to save config');
    } finally {
      setSaving(false);
    }
  }

  return (
    <section id="admin-tabpanel-config" role="tabpanel" aria-labelledby="admin-tab-config" className={visible ? 'block' : 'hidden'} hidden={!visible}>
      <SectionIntro label="Config" title="Runtime generation settings" note="Only safe runtime fields are editable. Secret values are never surfaced." />

      {error ? <NoticeBanner tone="alert">{error}</NoticeBanner> : null}
      {notice ? <NoticeBanner tone="confirm">{notice}</NoticeBanner> : null}

      <Panel className="p-5 sm:p-7">
        <PanelHeader
          label="Admin runtime config"
          note="Loaded from the data dir. Writes go back to the same file."
          action={
            <Button type="button" onClick={handleSave} disabled={saving || !draft || !dirty} variant="primary">
              {saving ? 'Saving…' : dirty ? 'Save config' : 'Saved'}
            </Button>
          }
        />

        {loading && !draft ? <p className="mt-5 text-sm text-ink-dim">Loading config…</p> : null}

        {draft ? (
          <div className="mt-6 grid gap-4 lg:grid-cols-2">
            <Field label="Owner" hint="Optional. Defaults to the primary repo owner.">
              <Input
                value={draft.generation.owner ?? ''}
                onChange={(event) =>
                  updateGeneration({ owner: event.target.value || undefined })
                }
                placeholder="subculture-collective"
              />
            </Field>

            <Field label="Repo" hint="Optional. Leaves the owner-wide scan behavior intact.">
              <Input
                value={draft.generation.repo ?? ''}
                onChange={(event) =>
                  updateGeneration({ repo: event.target.value || undefined })
                }
                placeholder=""
              />
            </Field>

            <Field label="LLM provider" hint="Optional. Defaults to CHANGEMAKER_LLM_PROVIDER or openai.">
              <Input value={draft.generation.llmProvider ?? ''} onChange={(event) => updateGeneration({ llmProvider: event.target.value || undefined })} placeholder="openai" />
            </Field>

            <Field label="LLM model" hint="Optional. Defaults to CHANGEMAKER_LLM_MODEL from the environment when blank.">
              <Input value={draft.generation.llmModel ?? ''} onChange={(event) => updateGeneration({ llmModel: event.target.value || undefined })} placeholder="gpt-4.1-mini" />
            </Field>

            <ToggleField
              label="Summarize"
              checked={draft.generation.summarize}
              onChange={(checked) => updateGeneration({ summarize: checked })}
              hint="Runs the summarizer pipeline before posting."
            />

            <ToggleField
              label="Post"
              checked={draft.generation.post}
              onChange={(checked) => updateGeneration({ post: checked })}
              hint="Controls whether the generated report is written out."
            />

            <ToggleField
              label="Mark covered"
              checked={draft.generation.markCovered}
              onChange={(checked) => updateGeneration({ markCovered: checked })}
              hint="Marks clustered changes as handled after a run."
            />

            <ToggleField label="Include covered" checked={draft.generation.includeCovered} onChange={(checked) => updateGeneration({ includeCovered: checked })} hint="Includes changes already linked to prior posts." />

            <ToggleField label="Fetch diffs" checked={draft.generation.includeDiffs} onChange={(checked) => updateGeneration({ includeDiffs: checked })} hint="Fetches PR and commit diffs for richer summaries." />

            <ToggleField label="Update narrative memory" checked={draft.generation.narrativeUpdate} onChange={(checked) => updateGeneration({ narrativeUpdate: checked })} hint="Updates the narrative state after writing a post." />

            <ToggleField label="Social section" checked={draft.generation.socialSection} onChange={(checked) => updateGeneration({ socialSection: checked })} hint="Adds a separate Twitter/X section with suggested posting times." />

            <Field label="Summary retries" hint="Number of retries before the run gives up.">
              <Input
                type="number"
                min={1}
                step={1}
                value={draft.generation.summaryRetries}
                onChange={(event) =>
                  updateGeneration({ summaryRetries: Number.parseInt(event.target.value, 10) || 1 })
                }
              />
            </Field>

            <Field label="Lookback hours" hint="Default activity scan window for daily posts.">
              <Input type="number" min={1} step={1} value={draft.generation.lookbackHours} onChange={(event) => updateGeneration({ lookbackHours: Number.parseFloat(event.target.value) || 1 })} />
            </Field>

            <Field label="Cadence hours" hint="Nominal publish cadence used when calculating automatic daily windows.">
              <Input type="number" min={1} step={1} value={draft.generation.cadenceHours} onChange={(event) => updateGeneration({ cadenceHours: Number.parseFloat(event.target.value) || 1 })} />
            </Field>

            <Field label="Max diff chars" hint="Maximum filtered diff characters per change.">
              <Input type="number" min={1} step={1000} value={draft.generation.maxDiffChars} onChange={(event) => updateGeneration({ maxDiffChars: Number.parseInt(event.target.value, 10) || 1 })} />
            </Field>

            <Field label="Report date offset" hint="Days to subtract from publish date for the post title/header. Use 1 for posts published the day after the work.">
              <Input type="number" min={0} step={1} value={draft.generation.reportDateOffsetDays} onChange={(event) => updateGeneration({ reportDateOffsetDays: Number.parseInt(event.target.value, 10) || 0 })} />
            </Field>

            <PeriodField draft={draft} updateGeneration={updateGeneration} />
          </div>
        ) : null}
      </Panel>
    </section>
  );
}

function Field({ label, hint, children }: { label: string; hint: string; children: ReactNode }) {
  return (
    <label className="block">
      <div className="mb-2 flex items-end justify-between gap-3">
        <span className="text-sm font-medium text-ink">{label}</span>
      </div>
      {children}
      <p className="mt-2 text-xs leading-5 text-ink-dim">{hint}</p>
    </label>
  );
}

function ToggleField({
  label,
  hint,
  checked,
  onChange,
}: {
  label: string;
  hint: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <label className="flex cursor-pointer flex-col border border-border-faint bg-panel p-4 transition duration-100 hover:border-pulse hover:bg-panel-raised">
      <div className="flex items-center justify-between gap-4">
        <span className="text-sm font-medium text-ink">{label}</span>
        <input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} className="h-4 w-4 accent-pulse" />
      </div>
      <p className="mt-2 text-xs leading-5 text-ink-dim">{hint}</p>
    </label>
  );
}

function PeriodField({
  draft,
  updateGeneration,
}: {
  draft: RuntimeConfig;
  updateGeneration: (patch: Partial<RuntimeConfig['generation']>) => void;
}) {
  const periods: Array<'weekly' | 'monthly' | 'quarterly'> = ['weekly', 'monthly', 'quarterly'];

  return (
    <div className="border border-border-faint bg-panel p-4 lg:col-span-2">
      <span className="text-sm font-medium text-ink">Additional period posts</span>
      <p className="mt-2 text-xs leading-5 text-ink-dim">
        Generates extra weekly, monthly, and quarterly posts on due days without replacing the daily post.
      </p>
      <div className="mt-4 grid gap-3 sm:grid-cols-3">
        {periods.map((period) => (
          <label key={period} className="flex cursor-pointer items-center justify-between border border-border-faint bg-panel-raised p-3 text-sm capitalize text-ink">
            {period}
            <input
              type="checkbox"
              checked={draft.generation.extraPeriods.includes(period)}
              onChange={(event) => {
                const next = event.target.checked
                  ? [...draft.generation.extraPeriods, period]
                  : draft.generation.extraPeriods.filter((item) => item !== period);
                updateGeneration({ extraPeriods: Array.from(new Set(next)) });
              }}
              className="h-4 w-4 accent-pulse"
            />
          </label>
        ))}
      </div>
    </div>
  );
}
