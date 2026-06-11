import { useEffect, useMemo, useState } from 'react';
import { ApiError, type ApiClient } from '../api';
import { PROMPT_DEFINITIONS, type PromptFileName } from '../types';
import { Button, MicroLabel, NoticeBanner, Panel, PanelHeader, SectionIntro, SelectableCard, Textarea } from './ui';

type PromptEditorProps = {
  api: ApiClient | null;
  visible: boolean;
  onUnauthorized: () => void;
};

export function PromptEditor({ api, visible, onUnauthorized }: PromptEditorProps) {
  const [selectedFile, setSelectedFile] = useState<PromptFileName>(PROMPT_DEFINITIONS[0].fileName);
  const [drafts, setDrafts] = useState<Partial<Record<PromptFileName, string>>>({});
  const [saved, setSaved] = useState<Partial<Record<PromptFileName, string>>>({});
  const [loadingFile, setLoadingFile] = useState<PromptFileName | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    if (!visible || !api) return;
    if (drafts[selectedFile] !== undefined && saved[selectedFile] !== undefined) return;

    let cancelled = false;
    setLoadingFile(selectedFile);
    setError(null);

    void api
      .readPrompt(selectedFile)
      .then((content) => {
        if (cancelled) return;
        setDrafts((current) => (current[selectedFile] === undefined ? { ...current, [selectedFile]: content } : current));
        setSaved((current) => ({ ...current, [selectedFile]: content }));
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 401) {
          onUnauthorized();
          return;
        }
        setError(err instanceof Error ? err.message : 'Failed to load prompt');
      })
      .finally(() => {
        if (!cancelled) setLoadingFile(null);
      });

    return () => {
      cancelled = true;
    };
  }, [api, drafts, onUnauthorized, saved, selectedFile, visible]);

  const currentDefinition = useMemo(
    () => PROMPT_DEFINITIONS.find((item) => item.fileName === selectedFile) ?? PROMPT_DEFINITIONS[0],
    [selectedFile],
  );

  const currentDraft = drafts[selectedFile] ?? '';
  const currentSaved = saved[selectedFile] ?? '';
  const dirty = drafts[selectedFile] !== undefined && currentDraft !== currentSaved;

  async function handleSave() {
    if (!api) return;

    setSaving(true);
    setError(null);
    setNotice(null);

    try {
      await api.savePrompt(selectedFile, currentDraft);
      setSaved((current) => ({ ...current, [selectedFile]: currentDraft }));
      setNotice(`${currentDefinition.title} saved.`);
    } catch (err: unknown) {
      if (err instanceof ApiError && err.status === 401) {
        onUnauthorized();
        return;
      }
      setError(err instanceof Error ? err.message : 'Failed to save prompt');
    } finally {
      setSaving(false);
    }
  }

  return (
    <section id="admin-tabpanel-prompts" role="tabpanel" aria-labelledby="admin-tab-prompts" className={visible ? 'block' : 'hidden'} hidden={!visible}>
      <SectionIntro label="Prompts" title="Edit runtime prompt files" note="Changes persist under the runtime data dir and never touch repo files." />

      {error ? <NoticeBanner tone="alert">{error}</NoticeBanner> : null}
      {notice ? <NoticeBanner tone="confirm">{notice}</NoticeBanner> : null}

      <div className="grid gap-4 xl:grid-cols-[0.9fr_1.1fr]">
        <Panel as="aside" className="p-4">
          <MicroLabel tone="pulse" className="mb-3">Prompt files</MicroLabel>
          <div className="space-y-2">
            {PROMPT_DEFINITIONS.map((definition) => {
              const active = definition.fileName === selectedFile;
              return (
                <SelectableCard
                  key={definition.fileName}
                  type="button"
                  onClick={() => setSelectedFile(definition.fileName)}
                  active={active}
                >
                  <div className="flex items-center justify-between gap-3">
                    <p className="text-sm font-medium">{definition.title}</p>
                    <span className="text-2xs uppercase tracking-[0.12em] text-ink-mute">{definition.fileName}</span>
                  </div>
                  <p className="mt-2 text-xs leading-5 text-ink-dim">{definition.description}</p>
                </SelectableCard>
              );
            })}
          </div>
        </Panel>

        <Panel className="p-5 sm:p-7">
          <PanelHeader
            label={currentDefinition.fileName}
            title={currentDefinition.title}
            note={currentDefinition.description}
            action={
              <div className="flex items-center gap-3">
                {loadingFile === selectedFile ? <span className="text-2xs uppercase tracking-[0.12em] text-ink-mute">Loading…</span> : null}
                <Button
                  type="button"
                  onClick={handleSave}
                  disabled={saving || !dirty}
                  variant="primary"
                >
                  {saving ? 'Saving…' : dirty ? 'Save prompt' : 'Saved'}
                </Button>
              </div>
            }
          />

          <label className="mt-5 block">
            <span className="sr-only">Prompt content</span>
            <Textarea
              value={currentDraft}
              onChange={(event) => setDrafts((current) => ({ ...current, [selectedFile]: event.target.value }))}
              spellCheck={false}
              className="min-h-[30rem] px-4 py-4 font-mono text-sm leading-6"
              placeholder="Prompt text"
            />
          </label>

          <p className="mt-4 text-sm text-ink-dim">Stored as plain text in the runtime data directory. No secrets shown here.</p>
        </Panel>
      </div>
    </section>
  );
}
