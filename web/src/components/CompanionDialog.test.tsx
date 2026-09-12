import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { I18nProvider } from '../i18n'
import type { Companion } from '../companion'
import { CompanionDialog } from './CompanionDialog'

describe('CompanionDialog', () => {
  let container: HTMLDivElement
  let mounted: Root
  let calls: { method: string; payload: Record<string, unknown> }[]
  let entries: Companion[]
  let conflict: boolean
  let held: Promise<void> | undefined
  const entry = (): Companion => ({ parentSessionId: 'parent', name: 'reviewer', childSessionId: 'child', configuration: 'independent-review', revision: 4, creationId: 'lifetime-1', enabled: true, removed: false, persisted: true, phase: 'ready', activity: 'idle', failure: false })

  beforeEach(() => {
    localStorage.setItem('goren.locale', 'en')
    container = document.createElement('div')
    document.body.append(container)
    mounted = createRoot(container)
    calls = []
    entries = [entry()]
    conflict = false
    held = undefined
    vi.stubGlobal('confirm', vi.fn(() => true))
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
      const body = JSON.parse(String(init.body)) as { rpcId: string; method: string; payload: Record<string, unknown> }
      calls.push({ method: body.method, payload: body.payload })
      let value: unknown
      if (body.method === 'settings.describe') value = { writable: true, namespaces: [
        { ns: 'companion', name: 'reviewer', revision: 1, value: { enabled: true }, applies: 'new_instance' },
        { ns: 'companion', name: 'researcher', revision: 1, value: { enabled: true }, applies: 'new_instance' },
        { ns: 'agent', name: 'independent-review', revision: 2, value: { prompts: { default: { persona: 'Review independently' } } }, applies: 'agent_restart' },
      ] }
      else if (body.method === 'companion.list') value = { phase: 'ready', failure: false, items: entries }
      else {
        await held
        if (conflict) return Response.json({ rpcId: body.rpcId, result: { ok: false, error: { code: 'companion-conflict', message: 'private diagnostic' } } })
        if (body.method === 'companion.configure') entries = entries.map(current => ({ ...current, enabled: Boolean(body.payload.enabled), revision: current.revision + 1, phase: body.payload.enabled ? 'ready' : 'stopped' }))
        if (body.method === 'companion.remove') entries = entries.map(current => ({ ...current, removed: true }))
        if (body.method === 'companion.restart') entries = [{ ...entry(), creationId: 'lifetime-2' }]
        if (body.method === 'companion.create') entries = [{ ...entry(), name: String(body.payload.name) }]
        value = entries[0]
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
  async function render(sessionId?: string): Promise<void> {
    await act(async () => mounted.render(<I18nProvider><CompanionDialog key={sessionId} sessionId={sessionId} onClose={() => {}} /></I18nProvider>))
  }
  const button = (text: string): HTMLButtonElement => {
    const result = [...container.querySelectorAll('button')].find(candidate => candidate.textContent === text)
    expect(result, text).toBeDefined()
    return result!
  }
  async function click(text: string): Promise<void> { await act(async () => button(text).click()) }

  it('requires a parent and never sends an unscoped management request', async () => {
    await render()
    expect(calls.map(call => call.method)).toEqual(['settings.describe'])
    expect(button('Create companion').disabled).toBe(true)
    expect(container.textContent).toContain('Select a parent session first')
  })

  it('uses observed revisions and creation identity for management', async () => {
    await render('parent')
    await click('Disable')
    expect(calls).toContainEqual({ method: 'companion.configure', payload: { parentSessionId: 'parent', name: 'reviewer', expectedRevision: 4, enabled: false } })
    await click('Enable')
    expect(calls).toContainEqual({ method: 'companion.configure', payload: { parentSessionId: 'parent', name: 'reviewer', expectedRevision: 5, enabled: true } })
    await click('Restart')
    expect(calls).toContainEqual({ method: 'companion.restart', payload: { parentSessionId: 'parent', name: 'reviewer', expectedRevision: 6, expectedCreationId: 'lifetime-1' } })
    await click('Remove')
    expect(calls.at(-2)?.method).toBe('settings.describe')
    expect(calls).toContainEqual({ method: 'companion.remove', payload: { parentSessionId: 'parent', name: 'reviewer', expectedRevision: 4 } })
    expect(container.querySelector('article')).toBeNull()
  })

  it('can restart failed preparation with revision zero and an empty creation ID', async () => {
    entries = [{ ...entry(), revision: 0, persisted: false, creationId: undefined, childSessionId: undefined, configuration: undefined, phase: 'start_failed', failure: true }]
    await render('parent')
    expect(container.textContent).toContain('Startup failed')
    expect(button('Disable').disabled).toBe(true)
    await click('Restart')
    expect(calls).toContainEqual({ method: 'companion.restart', payload: { parentSessionId: 'parent', name: 'reviewer', expectedRevision: 0, expectedCreationId: '' } })
  })

  it('does not repeat a conflicting mutation and hides private diagnostics', async () => {
    conflict = true
    await render('parent')
    await click('Restart')
    expect(calls.filter(call => call.method === 'companion.restart')).toHaveLength(1)
    expect(container.querySelector('[role="alert"]')).not.toBeNull()
    expect(container.textContent).not.toContain('private diagnostic')
  })

  it('prevents concurrent mutations while the first request settles', async () => {
    let release!: () => void
    held = new Promise<void>(resolve => { release = resolve })
    await render('parent')
    await click('Restart')
    expect(button('Disable').disabled).toBe(true)
    expect(button('Restart').disabled).toBe(true)
    await act(async () => release())
    expect(button('Restart').disabled).toBe(false)
  })

  it('opens the exact independent configuration instead of a global group', async () => {
    await render('parent')
    await click('Edit instance configuration')
    expect(container.querySelector('textarea')?.value).toBe('Review independently')
    expect(container.textContent).toContain('independent-review')
  })

  it('does not present an unavailable child as ready and keeps recovery accessible', async () => {
    entries = [{ ...entry(), activity: 'unavailable', failure: true }]
    await render('parent')
    expect(container.querySelector('article')?.textContent).toContain('Unavailable')
    expect(button('Restart').disabled).toBe(false)
  })

  it('creates from a saved policy using the current parent', async () => {
    entries = []
    await render('parent')
    const select = container.querySelector('select')!
    await act(async () => { select.value = 'researcher'; select.dispatchEvent(new Event('change', { bubbles: true })) })
    await click('Create companion')
    expect(calls).toContainEqual({ method: 'companion.create', payload: { parentSessionId: 'parent', name: 'researcher' } })
  })

  it('rejects a listing belonging to another parent', async () => {
    entries = [{ ...entry(), parentSessionId: 'another-parent' }]
    await render('parent')
    expect(container.querySelector('article')).toBeNull()
    expect(container.querySelector('[role="alert"]')).not.toBeNull()
    expect(button('Create companion').disabled).toBe(true)
  })
})
