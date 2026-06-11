import { NavLink, Outlet } from 'react-router-dom'
import { useEffect, useState } from 'react'
import { getAdminKey, setAdminKey } from '@/lib/api/client'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { cn } from '@/lib/utils'

const TABS = [
  { to: '/queue',   label: 'Queue' },
  { to: '/history', label: 'History' },
  { to: '/logs',    label: 'Logs' },
  { to: '/audit',   label: 'Audit' },
  { to: '/stats',   label: 'Stats' },
  { to: '/search',  label: 'Search' },
  { to: '/keys',    label: 'Keys' },
  { to: '/config',  label: 'Config' },
]

export function Layout() {
  const [keyInput, setKeyInput] = useState('')
  const [hasKey, setHasKey] = useState(false)
  const phoenixUrl = import.meta.env.VITE_PHOENIX_URL as string | undefined

  useEffect(() => { setHasKey(!!getAdminKey()) }, [])

  const saveKey = () => {
    setAdminKey(keyInput)
    setKeyInput('')
    setHasKey(!!getAdminKey())
    // trigger a soft refresh
    window.location.reload()
  }
  const clearKey = () => { setAdminKey(''); setHasKey(false); window.location.reload() }

  return (
    <div className="min-h-full flex flex-col">
      <header className="border-b border-[var(--color-border)] bg-[var(--color-card)]">
        <div className="max-w-[1400px] mx-auto px-4 py-3 flex items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <h1 className="text-lg font-semibold tracking-tight">llama-line</h1>
            <span className="text-xs text-[var(--color-muted)]">broker console</span>
          </div>
          <div className="flex items-center gap-2">
            {phoenixUrl ? <a href={phoenixUrl} target="_blank" rel="noopener noreferrer" className="text-xs text-[var(--color-muted)] hover:text-[var(--color-text)]">Phoenix</a> : null}
            {hasKey ? (
              <>
                <span className="text-xs text-[var(--color-success)]">admin key set</span>
                <Button size="sm" variant="ghost" onClick={clearKey}>Clear</Button>
              </>
            ) : (
              <>
                <Input
                  type="password"
                  placeholder="admin key"
                  value={keyInput}
                  onChange={(e) => setKeyInput(e.target.value)}
                  className="w-48"
                />
                <Button size="sm" onClick={saveKey} disabled={!keyInput}>Save</Button>
              </>
            )}
          </div>
        </div>
        <nav className="max-w-[1400px] mx-auto px-4 flex gap-1">
          {TABS.map(t => (
            <NavLink key={t.to} to={t.to}
              className={({ isActive }) => cn(
                'px-3 py-2 text-sm border-b-2 transition-colors',
                isActive
                  ? 'border-[var(--color-accent)] text-[var(--color-text)]'
                  : 'border-transparent text-[var(--color-muted)] hover:text-[var(--color-text)]',
              )}>
              {t.label}
            </NavLink>
          ))}
        </nav>
      </header>
      <main className="flex-1 max-w-[1400px] w-full mx-auto px-4 py-4">
        <Outlet />
      </main>
    </div>
  )
}
