import { useMemo, useState } from 'react'
import { cn } from '@/lib/utils'

type View = 'pretty' | 'chat' | 'raw'

interface Props {
  label: string
  body: string
  /** When true, default to 'chat' view if the body looks like a chat. */
  preferChat?: boolean
  className?: string
}

// BodyView shows a request/response body with three modes:
//  - pretty: JSON.stringify(parsed, null, 2) with light syntax highlighting
//  - chat:   render messages[] / message{} as a transcript when shape matches
//  - raw:    untouched original string
//
// Bodies that don't parse as JSON fall back to raw mode and disable the
// other tabs.
export function BodyView({ label, body, preferChat = true, className }: Props) {
  const parsed = useMemo(() => safeJSONParse(body), [body])
  const isJSON = parsed !== UNPARSEABLE
  const chatShape = isJSON ? detectChatShape(parsed) : null
  const initial: View = isJSON ? (preferChat && chatShape ? 'chat' : 'pretty') : 'raw'
  const [view, setView] = useState<View>(initial)
  const [wrap, setWrap] = useState(true)

  const copy = () => {
    if (view === 'pretty' && isJSON) navigator.clipboard?.writeText(JSON.stringify(parsed, null, 2))
    else navigator.clipboard?.writeText(body)
  }

  return (
    <details className={cn('group', className)}>
      <summary className="cursor-pointer list-none flex items-center gap-2 text-[var(--color-accent)] hover:text-[var(--color-accent-hover)] py-1">
        <span className="select-none text-[var(--color-muted)] group-open:rotate-90 transition-transform inline-block">▸</span>
        <span>{label}</span>
        <span className="text-[var(--color-muted)] text-xs">({body.length.toLocaleString()} chars{isJSON ? ', json' : ''})</span>
      </summary>
      <div className="mt-1 ml-4 space-y-1">
        <div className="flex items-center gap-1 text-xs">
          <TabBtn active={view==='chat'} disabled={!chatShape} onClick={() => setView('chat')}>chat</TabBtn>
          <TabBtn active={view==='pretty'} disabled={!isJSON} onClick={() => setView('pretty')}>pretty</TabBtn>
          <TabBtn active={view==='raw'} onClick={() => setView('raw')}>raw</TabBtn>
          <label className="ml-2 flex items-center gap-1 text-[var(--color-muted)]">
            <input type="checkbox" checked={wrap} onChange={e => setWrap(e.target.checked)} />
            wrap
          </label>
          <button onClick={copy} className="ml-auto text-[var(--color-accent)] hover:underline">copy</button>
        </div>
        {view === 'chat' && chatShape ? (
          <ChatTranscript chat={chatShape} />
        ) : view === 'pretty' && isJSON ? (
          <PrettyJSON value={parsed} wrap={wrap} />
        ) : (
          <pre
            className={cn(
              'text-[var(--color-text)] bg-[var(--color-card)] p-2 rounded-[var(--radius-sm)] border border-[var(--color-border)] max-h-[60vh] overflow-auto',
              wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre'
            )}
          >
            {body}
          </pre>
        )}
      </div>
    </details>
  )
}

function TabBtn({ active, disabled, onClick, children }: { active: boolean; disabled?: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      className={cn(
        'px-2 py-0.5 rounded border text-xs',
        active
          ? 'bg-[var(--color-accent)] border-[var(--color-accent)] text-white'
          : 'border-[var(--color-border)] text-[var(--color-muted)] hover:text-[var(--color-text)]',
        disabled && 'opacity-40 cursor-not-allowed hover:text-[var(--color-muted)]',
      )}
    >
      {children}
    </button>
  )
}

const UNPARSEABLE = Symbol('unparseable')
function safeJSONParse(s: string): unknown {
  try { return JSON.parse(s) } catch { return UNPARSEABLE }
}

// ───────────────── pretty JSON with light syntax colouring ─────────────────

function PrettyJSON({ value, wrap }: { value: unknown; wrap: boolean }) {
  const text = JSON.stringify(value, null, 2)
  // Tokenise minimally: strings (incl. keys), numbers, booleans/null, punctuation.
  // Distinguish keys ("…":) from values.
  const parts: Array<{ t: 'key'|'str'|'num'|'lit'|'punct'|'space' ; v: string }> = []
  const re = /("(?:\\.|[^"\\])*")(\s*:)?|(\b-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b)|(\btrue\b|\bfalse\b|\bnull\b)|([{}[\],])|(\s+)/g
  let m: RegExpExecArray | null
  let last = 0
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) parts.push({ t: 'punct', v: text.slice(last, m.index) })
    if (m[1]) {
      if (m[2]) { parts.push({ t: 'key', v: m[1] }); parts.push({ t: 'punct', v: m[2] }) }
      else parts.push({ t: 'str', v: m[1] })
    } else if (m[3]) parts.push({ t: 'num', v: m[3] })
    else if (m[4]) parts.push({ t: 'lit', v: m[4] })
    else if (m[5]) parts.push({ t: 'punct', v: m[5] })
    else if (m[6]) parts.push({ t: 'space', v: m[6] })
    last = re.lastIndex
  }
  if (last < text.length) parts.push({ t: 'punct', v: text.slice(last) })

  const color: Record<string, string> = {
    key: 'text-[var(--color-accent)]',
    str: 'text-[var(--color-success)]',
    num: 'text-[var(--color-warn)]',
    lit: 'text-[var(--color-info,#60a5fa)]',
    punct: 'text-[var(--color-muted)]',
    space: '',
  }

  return (
    <pre
      className={cn(
        'bg-[var(--color-card)] p-2 rounded-[var(--radius-sm)] border border-[var(--color-border)] max-h-[60vh] overflow-auto',
        wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre'
      )}
    >
      {parts.map((p, i) => (
        <span key={i} className={color[p.t]}>{p.v}</span>
      ))}
    </pre>
  )
}

// ───────────────── chat detection + transcript ─────────────────

type ChatMessage = { role: string; content: string; tool?: string; thinking?: string }
type ChatPayload = { kind: 'request' | 'response'; messages: ChatMessage[]; model?: string }

function detectChatShape(v: unknown): ChatPayload | null {
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>

  // Request shape: { messages: [...] }
  if (Array.isArray(o.messages)) {
    const msgs = (o.messages as unknown[]).map(toChatMessage).filter(Boolean) as ChatMessage[]
    if (msgs.length === 0) return null
    return { kind: 'request', messages: msgs, model: typeof o.model === 'string' ? o.model : undefined }
  }
  // Ollama non-streaming response: { message: { role, content, thinking? } }
  if (o.message && typeof o.message === 'object') {
    const m = toChatMessage(o.message)
    if (m) return { kind: 'response', messages: [m], model: typeof o.model === 'string' ? o.model : undefined }
  }
  // OpenAI-style: { choices: [{ message: {...} }] }
  if (Array.isArray(o.choices)) {
    const out: ChatMessage[] = []
    for (const c of o.choices as unknown[]) {
      if (c && typeof c === 'object') {
        const cm = (c as Record<string, unknown>).message
        const m = toChatMessage(cm)
        if (m) out.push(m)
      }
    }
    if (out.length) return { kind: 'response', messages: out, model: typeof o.model === 'string' ? o.model : undefined }
  }
  return null
}

function toChatMessage(v: unknown): ChatMessage | null {
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  const role = typeof o.role === 'string' ? o.role : ''
  if (!role) return null
  const content = typeof o.content === 'string' ? o.content : (o.content == null ? '' : JSON.stringify(o.content))
  const thinking = typeof o.thinking === 'string' ? o.thinking : undefined
  return { role, content, thinking }
}

const ROLE_STYLES: Record<string, string> = {
  system:    'border-l-[var(--color-muted)]',
  user:      'border-l-[var(--color-accent)]',
  assistant: 'border-l-[var(--color-success)]',
  tool:      'border-l-[var(--color-warn)]',
}

function ChatTranscript({ chat }: { chat: ChatPayload }) {
  return (
    <div className="space-y-2 max-h-[60vh] overflow-auto pr-1">
      {chat.model && (
        <div className="text-[10px] uppercase tracking-wide text-[var(--color-muted)]">
          {chat.kind} · model={chat.model}
        </div>
      )}
      {chat.messages.map((m, i) => (
        <div
          key={i}
          className={cn(
            'border-l-2 pl-2 py-1 bg-[var(--color-card)] rounded-r-[var(--radius-sm)]',
            ROLE_STYLES[m.role] ?? 'border-l-[var(--color-border)]',
          )}
        >
          <div className="flex items-center gap-2 text-[10px] uppercase tracking-wide text-[var(--color-muted)]">
            <span>{m.role}</span>
            {m.tool && <span>· {m.tool}</span>}
          </div>
          {m.thinking && (
            <details className="text-[var(--color-muted)] italic">
              <summary className="cursor-pointer text-xs">thinking ({m.thinking.length} chars)</summary>
              <pre className="whitespace-pre-wrap break-words text-xs mt-1">{m.thinking}</pre>
            </details>
          )}
          <pre className="whitespace-pre-wrap break-words text-[var(--color-text)] text-xs font-sans leading-relaxed">{m.content}</pre>
        </div>
      ))}
    </div>
  )
}
