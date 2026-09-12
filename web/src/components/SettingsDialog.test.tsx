import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { I18nProvider } from '../i18n'
import { SettingsDialog } from './SettingsDialog'
import type { SettingsGroup } from '../settings'
import { replaceTextareaValue } from '../test/dom'

const promptGroup = (): SettingsGroup => ({
  ns: 'prompt', name: 'shared', revision: 3, applies: 'agent_restart',
  base: { persona: 'local', includeHarnessIdentity: false },
  user: null, value: { persona: 'local', includeHarnessIdentity: false },
})

describe('SettingsDialog', () => {
  let container: HTMLDivElement
  let mounted: Root
  let calls: { method: string; payload: Record<string, unknown> }[]
  let groups: SettingsGroup[]
  let rejectWrite: boolean
  let writable: boolean

  beforeEach(() => {
    localStorage.setItem('goren.locale', 'en')
    container = document.createElement('div')
    document.body.append(container)
    mounted = createRoot(container)
    calls = []
    groups = [promptGroup(), { ns: 'tool', name: 'shared', revision: 2, applies: 'agent_restart', value: { enabled: true, visible: false }, base: {}, user: null }]
    rejectWrite = false
    writable = true
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
      const body = JSON.parse(String(init.body)) as { rpcId: string; method: string; payload: Record<string, unknown> }
      calls.push({ method: body.method, payload: body.payload })
      let value: unknown
      if (body.method === 'settings.describe') value = { writable, namespaces: groups }
      else if (body.method === 'credentials.describe') value = { credentials: { CUSTOM_KEY: { configured: false, writable: true } } }
      else if (body.method.startsWith('credentials.')) value = {}
      else {
        if (rejectWrite) return Response.json({ rpcId: body.rpcId, result: { ok: false, error: { code: 'settings-conflict', message: 'private-marker' } } })
        const kind = body.method.includes('Tool') ? 'tool' : body.method.includes('MCP') ? 'mcp' : body.method.includes('Provider') ? 'llmprovider' : 'prompt'
        const existing = groups.find(group => group.name === body.payload.name && group.ns === kind)
        const target = body.method.startsWith('settings.reset') ? existing?.base ?? null : body.payload.config as Record<string, unknown>
        const revision = Number(body.payload.expectedRevision) + 1
        const result: SettingsGroup = {
          ns: kind, name: String(body.payload.name), applies: kind === 'llmprovider' ? 'restart' : 'agent_restart',
          value: target, user: body.method.startsWith('settings.reset') ? null : target,
          base: existing?.base, revision, pendingRestart: kind === 'llmprovider',
        }
        groups = [...groups.filter(group => group.name !== result.name || group.ns !== result.ns), result]
        value = result
      }
      return Response.json({ rpcId: body.rpcId, result: { ok: true, value } })
    }))
  })

  afterEach(() => {
    act(() => mounted.unmount())
    container.remove()
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  async function renderDialog(): Promise<void> {
    await act(async () => mounted.render(<I18nProvider><SettingsDialog onClose={() => {}} /></I18nProvider>))
  }
  const button = (text: string): HTMLButtonElement => {
    const found = [...container.querySelectorAll<HTMLButtonElement>('button')].find(entry => entry.textContent === text)
    expect(found, text).toBeDefined()
    return found!
  }
  async function click(text: string): Promise<void> { await act(async () => button(text).click()) }
  async function fill(selector: string, value: string): Promise<void> {
    const input = container.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector)!
    expect(input).not.toBeNull()
    await act(async () => {
      if (input instanceof HTMLTextAreaElement) replaceTextareaValue(input, value)
      else {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
        input.dispatchEvent(new Event('input', { bubbles: true }))
      }
    })
  }
  async function save(): Promise<void> {
    await act(async () => container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
  }

  it('replaces one whole group, keeps untouched fields, and resets only its identity', async () => {
    await renderDialog()
    await fill('#settings-prompt-persona', 'new persona')
    expect(button('Toolsshared').disabled).toBe(true)
    await save()
    expect(calls).toContainEqual({ method: 'settings.updatePrompt', payload: { name: 'shared', expectedRevision: 3, config: { persona: 'new persona', includeHarnessIdentity: false } } })
    await click('Use group defaults')
    await save()
    expect(calls).toContainEqual({ method: 'settings.updatePrompt', payload: { name: 'shared', expectedRevision: 4, config: {} } })
    await click('Restore inheritance')
    expect(calls).toContainEqual({ method: 'settings.resetPrompt', payload: { name: 'shared', expectedRevision: 5 } })
    await click('Toolsshared')
    expect(container.querySelector('h3')?.textContent).toBe('Tools / shared')
    expect(container.querySelector('#settings-tool-visible')).not.toBeNull()
  })

  it('keeps line breaks while editing ordered tools and sends the complete order', async () => {
    groups[0] = { ...promptGroup(), value: { persona: 'local', toolOrder: ['<unlisted-tools>'] } }
    await renderDialog()
    await fill('#settings-prompt-toolOrder', '<unlisted-tools>\n')
    expect(container.querySelector<HTMLTextAreaElement>('#settings-prompt-toolOrder')!.value).toBe('<unlisted-tools>\n')
    await fill('#settings-prompt-toolOrder', 'read\n<unlisted-tools>\n')
    await save()
    expect(calls.at(-1)?.payload.config).toEqual({ persona: 'local', toolOrder: ['read', '<unlisted-tools>'] })
  })

  it('retains conflicted drafts and reloads the latest revision without a stale write', async () => {
    await renderDialog()
    await fill('#settings-prompt-persona', 'unsaved draft')
    rejectWrite = true
    await save()
    expect(container.querySelector<HTMLTextAreaElement>('#settings-prompt-persona')!.value).toBe('unsaved draft')
    expect(container.textContent).toContain('changed elsewhere')
    expect(container.textContent).not.toContain('private-marker')
    expect(button('Save configuration').disabled).toBe(true)
    groups[0] = { ...promptGroup(), revision: 8, value: { persona: 'another editor' } }
    rejectWrite = false
    await click('Discard draft and reload latest')
    await fill('#settings-prompt-persona', 'reconciled')
    await save()
    expect(calls.at(-1)).toEqual({ method: 'settings.updatePrompt', payload: { name: 'shared', expectedRevision: 8, config: { persona: 'reconciled' } } })
  })

  it('creates a complete named MCP group without the temporary connection API', async () => {
    await renderDialog()
    await click('Add MCP configuration')
    await fill('#settings-new-name', 'testa')
    await fill('#settings-mcp-connection-url', 'http://localhost:3000/mcp')
    await save()
    expect(calls.at(-1)).toEqual({ method: 'settings.updateMCP', payload: { name: 'testa', expectedRevision: 0, config: { enabled: true, connection: { serverName: 'testa', transport: 'streamable-http', url: 'http://localhost:3000/mcp' } } } })
    expect(calls.some(entry => entry.method === 'mcp.connect')).toBe(false)
  })

  it('uses the saved Provider reference for credentials and reports restart separately', async () => {
    groups = [{ ns: 'llmprovider', name: 'deepseek', revision: 0, applies: 'restart', value: { apiKeyEnv: 'CUSTOM_KEY', baseURL: 'https://example.test' }, base: {}, user: null }]
    await renderDialog()
    expect(calls).toContainEqual({ method: 'credentials.describe', payload: { refs: ['CUSTOM_KEY'] } })
    await fill('#settings-api-key', 'secret-marker')
    await act(async () => container.querySelectorAll('form')[1]!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(calls).toContainEqual({ method: 'credentials.set', payload: { ref: 'CUSTOM_KEY', value: 'secret-marker' } })
    expect(container.querySelector<HTMLInputElement>('#settings-api-key')!.value).toBe('')
    await fill('#settings-llmprovider-baseURL', 'https://changed.example.test')
    await save()
    expect(container.textContent).toContain('Restart the server')
    expect(JSON.stringify(calls.filter(entry => entry.method.startsWith('settings.')))).not.toContain('secret-marker')
  })

  it('keeps the request lifetime across parent renders and cancels waiting on unmount', async () => {
    await renderDialog()
    const signal = vi.mocked(fetch).mock.calls[0]![1]!.signal!
    await renderDialog()
    expect(signal.aborted).toBe(false)
    act(() => mounted.unmount())
    expect(signal.aborted).toBe(true)
    mounted = createRoot(container)
  })

  it('disables edits for a read-only catalog', async () => {
    writable = false
    await renderDialog()
    expect(container.querySelector('fieldset')!.disabled).toBe(true)
    expect(button('Save configuration').disabled).toBe(true)
    expect(container.textContent).toContain('read-only')
  })
})
