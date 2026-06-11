import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'
import { App } from '../App'

function mockSettings() {
  vi.stubGlobal('localStorage', {
    getItem: vi.fn(() =>
      JSON.stringify({
        baseUrl: 'http://gateway',
        apiKey: 'secret',
        refreshMs: 3000,
        theme: 'dark',
      }),
    ),
    setItem: vi.fn(),
  })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })

  return { promise, resolve, reject }
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

it('renders ops console navigation', () => {
  vi.stubGlobal('localStorage', {
    getItem: vi.fn(() => null),
    setItem: vi.fn(),
  })

  try {
    render(<App />)

    expect(screen.getByText('ops console')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Overview' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Requests' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Logs' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sessions' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Settings' })).toBeInTheDocument()
  } finally {
    vi.unstubAllGlobals()
  }
})

it('keeps the latest session selected when older loads resolve late', async () => {
  const user = userEvent.setup()
  mockSettings()

  const alpha = deferred<Response>()
  const beta = deferred<Response>()

  vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
    const url = String(input)

    if (url.endsWith('/ocq/metrics')) {
      return Promise.resolve(
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
    }

    if (url.endsWith('/ocq/requests')) {
      return Promise.resolve(new Response(JSON.stringify({ requests: [] }), { status: 200 }))
    }

    if (url.endsWith('/ocq/logs')) {
      return Promise.resolve(new Response(JSON.stringify({ logs: [] }), { status: 200 }))
    }

    if (url.endsWith('/ocq/sessions') && !url.includes('/ocq/sessions/')) {
      return Promise.resolve(
        new Response(
          JSON.stringify({
            sessions: [
              { id: 'ses_alpha', title: 'Alpha', createdAt: null, updatedAt: null },
              { id: 'ses_beta', title: 'Beta', createdAt: null, updatedAt: null },
            ],
          }),
          { status: 200 },
        ),
      )
    }

    if (url.endsWith('/ocq/sessions/ses_alpha')) return alpha.promise
    if (url.endsWith('/ocq/sessions/ses_beta')) return beta.promise

    throw new Error(`unexpected fetch ${url}`)
  })

  render(<App />)

  await user.click(screen.getByRole('button', { name: 'Sessions' }))
  await screen.findByText('Alpha')

  await user.click(screen.getByRole('button', { name: /Alpha/ }))
  await user.click(screen.getByRole('button', { name: /Beta/ }))

  beta.resolve(
    new Response(
      JSON.stringify({
        session: {
          id: 'ses_beta',
          title: 'Beta',
          createdAt: null,
          updatedAt: null,
          observeOnly: false,
          messages: [{ role: 'assistant', content: 'beta' }],
        },
        mode: 'chat',
        transport: 'stdio',
      }),
      { status: 200 },
    ),
  )

  await screen.findByRole('heading', { name: 'Beta' })

  alpha.resolve(
    new Response(
      JSON.stringify({
        session: {
          id: 'ses_alpha',
          title: 'Alpha',
          createdAt: null,
          updatedAt: null,
          observeOnly: false,
          messages: [{ role: 'assistant', content: 'alpha' }],
        },
        mode: 'chat',
        transport: 'stdio',
      }),
      { status: 200 },
    ),
  )

  await waitFor(() => {
    expect(screen.getByRole('heading', { name: 'Beta' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Alpha' })).not.toBeInTheDocument()
  })
})
