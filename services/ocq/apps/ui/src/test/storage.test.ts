import { expect, it } from 'vitest'
import { DEFAULT_SETTINGS, loadSettings, saveSettings } from '../lib/storage'

function createStorage() {
  const map = new Map<string, string>()

  return {
    getItem: (key: string) => map.get(key) ?? null,
    setItem: (key: string, value: string) => {
      map.set(key, value)
    },
    removeItem: (key: string) => {
      map.delete(key)
    },
    clear: () => {
      map.clear()
    },
  } as Storage
}

it('saves and loads settings', () => {
  const storage = createStorage()

  const settings = {
    baseUrl: 'http://x',
    apiKey: 'secret',
    refreshMs: 1000,
    theme: 'dark' as const,
  }

  saveSettings(settings, storage)

  expect(loadSettings(storage)).toEqual(settings)
})

it('falls back on invalid JSON', () => {
  const storage = createStorage()
  storage.setItem('ocq.ui.settings.v1', '{')

  expect(loadSettings(storage)).toEqual(DEFAULT_SETTINGS)
})
