function extractDeltaContent(payload: unknown) {
  if (!payload || typeof payload !== 'object') return undefined
  const choices = (payload as { choices?: Array<{ delta?: { content?: unknown } }> }).choices
  const token = choices?.[0]?.delta?.content
  return typeof token === 'string' ? token : undefined
}

export async function streamChatCompletion(
  baseUrl: string,
  apiKey: string,
  body: unknown,
  onToken: (token: string) => void,
) {
  const payload = typeof body === 'object' && body !== null ? body : {}

  const response = await fetch(`${baseUrl.replace(/\/+$/, '')}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
    },
    body: JSON.stringify({ ...(payload as Record<string, unknown>), stream: true }),
  })

  if (!response.ok || !response.body) throw new Error(`stream failed: HTTP ${response.status}`)

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  while (true) {
    const { value, done } = await reader.read()
    if (done) break

    buffer += decoder.decode(value, { stream: true })
    const events = buffer.split(/\r?\n\r?\n/)
    buffer = events.pop() || ''

    for (const event of events) {
      const lines = event.split(/\r?\n/).filter(Boolean)
      for (const line of lines) {
        if (!line.startsWith('data:')) continue
        const data = line.slice(5).trimStart()
        if (data === '[DONE]') return

        const parsed = JSON.parse(data)
        const token = extractDeltaContent(parsed)
        if (token) onToken(token)
      }
    }
  }
}
