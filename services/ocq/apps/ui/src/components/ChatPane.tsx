import { useEffect, useMemo, useState, type KeyboardEvent } from 'react'
import { Send } from 'lucide-react'
import type { SessionDetail } from '../lib/types'
import { StatusBadge } from './StatusBadge'

type ChatPaneProps = {
  session?: SessionDetail
  onSend: (prompt: string) => Promise<void>
}

function roleLabel(role: string) {
  switch (role) {
    case 'user':
      return 'you'
    case 'assistant':
      return 'assistant'
    case 'system':
      return 'system'
    default:
      return role
  }
}

function bubbleTone(role: string) {
  if (role === 'user') return 'ml-auto border-cyan-400/20 bg-cyan-400/10 text-cyan-50'
  if (role === 'assistant') return 'border-slate-700 bg-slate-900/80 text-slate-100'
  return 'border-amber-400/20 bg-amber-400/10 text-amber-50'
}

export function ChatPane({ session, onSend }: ChatPaneProps) {
  const [prompt, setPrompt] = useState('')
  const [sending, setSending] = useState(false)

  useEffect(() => {
    setPrompt('')
    setSending(false)
  }, [session?.id])

  const canSend = useMemo(
    () => Boolean(session && !session.observeOnly && prompt.trim() && !sending),
    [prompt, session, sending],
  )

  async function submit() {
    if (!session || session.observeOnly || !prompt.trim() || sending) return

    setSending(true)
    try {
      await onSend(prompt.trim())
      setPrompt('')
    } finally {
      setSending(false)
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && (event.metaKey || event.ctrlKey) && canSend) {
      event.preventDefault()
      void submit()
    }
  }

  if (!session) {
    return (
      <section className="rounded-3xl border border-dashed border-slate-800 bg-slate-950/55 p-8 text-sm text-slate-400 backdrop-blur">
        <p className="text-xs uppercase tracking-[0.35em] text-slate-500">chat</p>
        <h2 className="mt-3 text-2xl font-semibold tracking-tight text-slate-50">Select a session.</h2>
        <p className="mt-3 max-w-xl leading-6">Pick one on the left to inspect its transcript in a safe read-only pane.</p>
      </section>
    )
  }

  const modeTone = session.observeOnly ? 'warning' : 'success'

  return (
    <section className="flex min-h-[40rem] flex-col overflow-hidden rounded-3xl border border-slate-800/90 bg-slate-950/75 shadow-[0_0_0_1px_rgba(15,23,42,0.55)] backdrop-blur">
      <header className="border-b border-slate-800/90 px-5 py-4 sm:px-6">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-xs uppercase tracking-[0.35em] text-cyan-300/70">session chat</p>
            <h2 className="mt-2 truncate text-2xl font-semibold tracking-tight text-slate-50">{session.title}</h2>
            <p className="mt-2 text-sm leading-6 text-slate-400">
              <span className="font-mono text-slate-500">{session.id}</span> · safe watch, detail refreshed by GET polling
            </p>
          </div>

          <StatusBadge tone={modeTone}>{session.observeOnly ? 'observe only' : 'interactive'}</StatusBadge>
        </div>

        <div className="mt-4 flex flex-wrap items-center gap-2 text-xs text-slate-500">
          <span className="rounded-full border border-slate-800 bg-slate-900/60 px-2 py-1 uppercase tracking-[0.28em]">
            {session.messages.length} messages
          </span>
          <span className="rounded-full border border-slate-800 bg-slate-900/60 px-2 py-1 uppercase tracking-[0.28em]">
            {session.observeOnly ? 'read only' : 'mutations allowed by send'}
          </span>
        </div>
      </header>

      <div className="flex-1 space-y-3 overflow-y-auto px-4 py-4 sm:px-6">
        {session.messages.length ? (
          session.messages.map((message, index) => (
            <article
              key={message.id ?? `${message.role}-${index}`}
              className={`max-w-[46rem] rounded-2xl border px-4 py-3 shadow-sm ${bubbleTone(message.role)}`}
            >
              <div className="flex items-center justify-between gap-3">
                <p className="text-[11px] uppercase tracking-[0.32em] text-current/70">{roleLabel(message.role)}</p>
                <span className="text-[11px] uppercase tracking-[0.26em] text-current/45">{message.id ?? 'stream'}</span>
              </div>
              <p className="mt-3 whitespace-pre-wrap break-words text-sm leading-6">{message.content}</p>
            </article>
          ))
        ) : (
          <div className="flex h-full items-center justify-center rounded-2xl border border-dashed border-slate-800/70 bg-slate-900/30 p-8 text-center text-sm text-slate-500">
            No messages in this session yet.
          </div>
        )}
      </div>

      <footer className="border-t border-slate-800/90 bg-slate-950/95 px-4 py-4 sm:px-6">
        {session.observeOnly ? (
          <div className="mb-3 rounded-2xl border border-amber-400/20 bg-amber-400/10 px-4 py-3 text-sm text-amber-100">
            Observe-only session. Transcript available, send disabled.
          </div>
        ) : null}

        <label className="grid gap-2">
          <span className="text-xs uppercase tracking-[0.32em] text-slate-500">Send prompt</span>
          <textarea
            className="min-h-28 rounded-2xl border border-slate-800 bg-slate-950 px-4 py-3 text-sm leading-6 text-slate-100 transition placeholder:text-slate-600 focus-visible:border-cyan-400/50 focus-visible:ring-2 focus-visible:ring-cyan-400/20 disabled:cursor-not-allowed disabled:opacity-50"
            value={prompt}
            onChange={(event) => setPrompt(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder={session.observeOnly ? 'Observe-only session' : 'Send a prompt to this session'}
            disabled={session.observeOnly || sending}
            aria-disabled={session.observeOnly || sending}
            spellCheck={false}
          />
        </label>

        <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
          <p className="text-xs text-slate-500">Cmd/Ctrl+Enter to send.</p>
          <button
            type="button"
            onClick={() => void submit()}
            disabled={!canSend}
            className="inline-flex items-center gap-2 rounded-full border border-cyan-400/20 bg-cyan-400/10 px-4 py-2 text-sm font-medium text-cyan-100 transition hover:bg-cyan-400/15 focus-visible:ring-2 focus-visible:ring-cyan-400/40 disabled:cursor-not-allowed disabled:opacity-50"
          >
            <Send className="h-4 w-4" />
            {sending ? 'Sending…' : 'Send'}
          </button>
        </div>
      </footer>
    </section>
  )
}
