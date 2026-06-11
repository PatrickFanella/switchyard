import { expect, it, vi } from 'vitest'
import { createGatewayClient, GatewayError } from '../lib/api'

it('sends bearer auth to metrics endpoint', async () => {
  const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    new Response(
      JSON.stringify({
        uptimeSeconds: 1,
        requestsTotal: 0,
        errorsTotal: 0,
        activeStreams: 0,
        p95LatencyMs: 0,
        latencyBuckets: [],
      }),
      { status: 200 },
    ),
  )

  const client = createGatewayClient('http://gateway/', 'secret')
  await client.metrics()

  expect(fetchMock).toHaveBeenCalledWith(
    'http://gateway/ocq/metrics',
    expect.objectContaining({
      headers: expect.objectContaining({ authorization: 'Bearer secret' }),
    }),
  )

  fetchMock.mockRestore()
})

it('sends session message body and throws gateway errors', async () => {
  const fetchMock = vi
    .spyOn(globalThis, 'fetch')
    .mockResolvedValueOnce(new Response(JSON.stringify({ sessionID: 's1', text: 'ok' }), { status: 200 }))
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ error: { message: 'nope' } }), { status: 400, statusText: 'Bad Request' }),
    )

  const client = createGatewayClient('http://gateway', 'secret')
  await client.sendSessionMessage('abc', 'hello')

  expect(fetchMock).toHaveBeenCalledWith(
    'http://gateway/ocq/sessions/abc/messages',
    expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ prompt: 'hello' }),
    }),
  )

  await expect(client.metrics()).rejects.toMatchObject({
    name: 'GatewayError',
    status: 400,
    message: 'nope',
  })

  fetchMock.mockRestore()
})
