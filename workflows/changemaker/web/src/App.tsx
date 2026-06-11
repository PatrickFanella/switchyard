import { useCallback, useEffect, useMemo, useState, type KeyboardEvent } from 'react';
import { clearStoredToken, createApi, readStoredToken, saveStoredToken, type ApiClient } from './api';
import { ConfigEditor } from './components/ConfigEditor';
import { PostReader } from './components/PostReader';
import { PromptEditor } from './components/PromptEditor';
import { RunControl } from './components/RunControl';
import { ServiceControl } from './components/ServiceControl';
import { Badge, Button, Input, MicroLabel, NoticeBanner, Panel, StatusLed, TabButton } from './components/ui';

const TABS = [
  { id: 'posts', label: 'Posts' },
  { id: 'prompts', label: 'Prompts' },
  { id: 'config', label: 'Config' },
  { id: 'runs', label: 'Runs' },
  { id: 'service', label: 'Service' },
] as const;

type TabId = (typeof TABS)[number]['id'];

export function App() {
  const [tokenDraft, setTokenDraft] = useState('');
  const [token, setToken] = useState(() => readStoredToken());
  const [activeTab, setActiveTab] = useState<TabId>('posts');
  const [banner, setBanner] = useState<string | null>(null);

  useEffect(() => {
    setTokenDraft('');
    document.title = 'Changemaker Admin';
  }, []);

  const handleUnauthorized = useCallback(() => {
    clearStoredToken();
    setToken('');
    setTokenDraft('');
    setActiveTab('posts');
    setBanner('Session expired. Re-enter the admin token.');
  }, []);

  const api: ApiClient | null = useMemo(() => {
    if (!token) return null;
    return createApi({ token, onUnauthorized: handleUnauthorized });
  }, [handleUnauthorized, token]);

  const savedToken = Boolean(token);

  function handleSaveToken() {
    const next = tokenDraft.trim();
    if (!next) return;

    saveStoredToken(next);
    setToken(next);
    setTokenDraft('');
    setBanner('Token saved locally in this browser.');
  }

  function handleForgetToken() {
    clearStoredToken();
    setToken('');
    setTokenDraft('');
    setBanner('Token cleared.');
  }

  function focusTab(tabId: TabId) {
    window.requestAnimationFrame(() => document.getElementById(tabButtonId(tabId))?.focus());
  }

  function handleTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, tabId: TabId) {
    const index = TABS.findIndex((tab) => tab.id === tabId);
    if (index === -1) return;

    const lastIndex = TABS.length - 1;
    const nextByKey: Partial<Record<string, number>> = {
      ArrowLeft: index === 0 ? lastIndex : index - 1,
      ArrowRight: index === lastIndex ? 0 : index + 1,
      Home: 0,
      End: lastIndex,
    };

    const nextIndex = nextByKey[event.key];
    if (nextIndex === undefined) return;

    event.preventDefault();
    const nextTab = TABS[nextIndex].id;
    setActiveTab(nextTab);
    focusTab(nextTab);
  }

  return (
    <main className="hud-scan min-h-screen bg-void font-body text-ink">
      <div className="relative z-10 mx-auto flex min-h-screen w-full max-w-7xl flex-col gap-4 px-4 py-4 sm:px-6 lg:px-8 lg:py-6">
        <header className="hud-panel hud-bracket flex flex-col gap-5 p-4 sm:p-5 lg:flex-row lg:items-start lg:justify-between">
          <div className="max-w-2xl">
            <div className="flex items-center gap-2">
              <StatusLed state={savedToken ? 'ok' : 'warn'} />
              <MicroLabel tone="signal">Changemaker admin</MicroLabel>
            </div>
            <h1 className="mt-2 text-3xl font-extrabold tracking-[-0.03em] text-ink sm:text-4xl">Token-gated control room</h1>
            <p className="mt-3 max-w-xl text-sm leading-6 text-ink-dim">
              Browse generated posts, edit runtime prompts, tune config, trigger runs, and manage service state.
            </p>
          </div>

          <div className="flex flex-col gap-3 sm:min-w-[22rem]">
            <div className="border border-border-faint bg-void-800 p-3">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <MicroLabel>Admin token</MicroLabel>
                  <p className="mt-2 text-sm text-ink-dim">Stored locally only.</p>
                </div>
                <Badge tone={savedToken ? 'confirm' : 'dead'}>{savedToken ? 'Unlocked' : 'Locked'}</Badge>
              </div>

              <div className="mt-4 flex gap-3">
                <Input
                  value={tokenDraft}
                  onChange={(event) => setTokenDraft(event.target.value)}
                  className="flex-1"
                  type="password"
                  placeholder={savedToken ? 'Enter a replacement token' : 'Paste admin token'}
                  autoComplete="off"
                  spellCheck={false}
                />
                <Button
                  type="button"
                  onClick={handleSaveToken}
                  disabled={!tokenDraft.trim()}
                  variant="primary"
                  className="px-4"
                >
                  Unlock
                </Button>
              </div>

              <div className="mt-3 flex items-center justify-between gap-3 text-2xs uppercase tracking-[0.12em] text-ink-mute">
                <span>Never shown after save.</span>
                {savedToken ? (
                  <button type="button" onClick={handleForgetToken} className="text-ink-dim transition hover:text-ink focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-pulse">
                    Forget token
                  </button>
                ) : null}
              </div>
            </div>

            {banner ? <NoticeBanner tone="pulse">{banner}</NoticeBanner> : null}
          </div>
        </header>

        {!savedToken ? (
          <Panel as="section" className="flex min-h-[24rem] flex-col items-center justify-center px-6 py-12 text-center">
            <MicroLabel tone="caution">Locked</MicroLabel>
            <h2 className="mt-3 text-3xl font-extrabold tracking-[-0.02em] text-ink">Enter the admin token to continue</h2>
            <p className="mt-3 max-w-2xl text-sm leading-7 text-ink-dim">
              The token stays in your browser localStorage. Nothing loads until you unlock the dashboard.
            </p>
          </Panel>
        ) : (
          <div key={token} className="flex flex-1 flex-col gap-6">
            <nav className="hud-panel flex flex-wrap gap-2 p-3" role="tablist" aria-label="Admin dashboard tabs">
              {TABS.map((tab) => {
                const active = activeTab === tab.id;
                return (
                  <TabButton
                    key={tab.id}
                    type="button"
                    role="tab"
                    id={tabButtonId(tab.id)}
                    aria-controls={tabPanelId(tab.id)}
                    aria-selected={active}
                    tabIndex={active ? 0 : -1}
                    active={active}
                    label={tab.label}
                    onClick={() => setActiveTab(tab.id)}
                    onKeyDown={(event) => handleTabKeyDown(event, tab.id)}
                  />
                );
              })}
            </nav>

            <div className="grid gap-6">
              <PostReader api={api} visible={activeTab === 'posts'} onUnauthorized={handleUnauthorized} />
              <PromptEditor api={api} visible={activeTab === 'prompts'} onUnauthorized={handleUnauthorized} />
              <ConfigEditor api={api} visible={activeTab === 'config'} onUnauthorized={handleUnauthorized} />
              <RunControl api={api} visible={activeTab === 'runs'} onUnauthorized={handleUnauthorized} />
              <ServiceControl api={api} visible={activeTab === 'service'} onUnauthorized={handleUnauthorized} />
            </div>
          </div>
        )}
      </div>
    </main>
  );
}

function tabButtonId(tabId: TabId): string {
  return `admin-tab-${tabId}`;
}

function tabPanelId(tabId: TabId): string {
  return `admin-tabpanel-${tabId}`;
}
