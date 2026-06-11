import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { ChatPane } from '../components/ChatPane'

it('marks observe-only sessions and disables sending', () => {
  render(
    <ChatPane
      session={{
        id: 'ses_1',
        title: 'Remote',
        createdAt: null,
        updatedAt: null,
        observeOnly: true,
        messages: [{ role: 'assistant', content: 'working' }],
      }}
      onSend={vi.fn()}
    />,
  )

  expect(screen.getByText(/observe-only session/i)).toBeInTheDocument()
  expect(screen.getByPlaceholderText('Observe-only session')).toBeDisabled()
  expect(screen.getByRole('button', { name: /send/i })).toBeDisabled()
})

it('sends prompts for interactive sessions', async () => {
  const user = userEvent.setup()
  const onSend = vi.fn().mockResolvedValue(undefined)

  const { getByRole, getByPlaceholderText } = render(
    <ChatPane
      session={{
        id: 'ses_2',
        title: 'Active',
        createdAt: null,
        updatedAt: null,
        observeOnly: false,
        messages: [],
      }}
      onSend={onSend}
    />,
  )

  await user.type(getByPlaceholderText('Send a prompt to this session'), 'hello there')
  await user.click(getByRole('button', { name: /^Send$/ }))

  expect(onSend).toHaveBeenCalledWith('hello there')
  expect(screen.getByPlaceholderText('Send a prompt to this session')).toHaveValue('')
})
