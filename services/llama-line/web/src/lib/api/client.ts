// REST client with admin-key injection and typed errors.

const ADMIN_KEY_STORAGE = 'llama_line_admin_key'

export function getAdminKey(): string {
  return localStorage.getItem(ADMIN_KEY_STORAGE) || ''
}

export function setAdminKey(value: string): void {
  if (value) localStorage.setItem(ADMIN_KEY_STORAGE, value)
  else localStorage.removeItem(ADMIN_KEY_STORAGE)
}

export class ApiError extends Error {
  status: number
  body: string
  constructor(status: number, message: string, body = '') {
    super(message)
    this.status = status
    this.body = body
    this.name = 'ApiError'
  }
  get isAuth() { return this.status === 401 || this.status === 403 }
  get isNotFound() { return this.status === 404 }
}

type RequestOpts = {
  method?: string
  body?: unknown
  signal?: AbortSignal
  /** Send X-Admin-Key header. Defaults to true for /admin/*. */
  admin?: boolean
}

export async function request<T>(path: string, opts: RequestOpts = {}): Promise<T> {
  const { method = 'GET', body, signal } = opts
  const wantsAdmin = opts.admin ?? path.startsWith('/admin')

  const headers: Record<string, string> = {}
  if (wantsAdmin) {
    const key = getAdminKey()
    if (key) headers['X-Admin-Key'] = key
  }
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })

  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new ApiError(res.status, `${method} ${path} → ${res.status}`, text)
  }
  // 204 / empty
  if (res.status === 204) return undefined as T
  const ctype = res.headers.get('content-type') || ''
  if (ctype.includes('application/json')) return (await res.json()) as T
  return (await res.text()) as unknown as T
}
