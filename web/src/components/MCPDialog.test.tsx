import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { I18nProvider } from '../i18n'
import { MCPDialog } from './MCPDialog'

describe('MCPDialog', () => {
  let container: HTMLDivElement
  let mounted: Root
  let calls: { method: string; payload: unknown }[]
  let connections: object[]
  let rejectConnect: boolean

  beforeEach(() => {
    container = document.createElement('div')
    document.body.append(container)
    mounted = createRoot(container)
    calls = []
    connections = []
    rejectConnect = false
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
      const body = JSON.parse(String(init.body)) as { rpcId: string; method: string; payload: unknown }
      calls.push({ method: body.method, payload: body.payload })
      let value: unknown = connections
      if (body.method === 'mcp.connect') {
        if (rejectConnect) return Response.json({ rpcId: body.rpcId, result: { ok: false, error: { message: 'private endpoint details' } } })
        const request = body.payload as { serverName: string }
        value = { serverName: request.serverName, transport: 'streamable-http', status: 'connected', toolCount: 2 }
        connections = [value as object]
      }
      if (body.method === 'mcp.disconnect') {
        connections = []
        value = {}
      }
      return Response.json({ rpcId: body.rpcId, result: { ok: true, value } })
    }))
  })

  afterEach(() => {
    act(() => mounted.unmount())
    vi.unstubAllGlobals()
  })

  async function renderDialog(): Promise<void> {
    await act(async () => mounted.render(<I18nProvider><MCPDialog onClose={() => {}} /></I18nProvider>))
  }

  async function fill(selector: string, value: string): Promise<void> {
    const input = container.querySelector<HTMLInputElement>(selector)!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }

  async function connectServer(): Promise<void> {
    await fill('#mcp-name', 'fixture')
    await fill('#mcp-url', 'http://localhost:3000/mcp')
    await act(async () => container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
  }

  it('connects an HTTP URL, displays tools and disconnects without a page reload', async () => {
    await renderDialog()
    await connectServer()
    expect(calls).toContainEqual({ method: 'mcp.connect', payload: { serverName: 'fixture', url: 'http://localhost:3000/mcp' } })
    expect(container.textContent).toContain('fixture')
    expect(container.textContent).toContain('2')
    expect(container.querySelector<HTMLInputElement>('#mcp-url')!.value).toBe('')
    await act(async () => container.querySelector<HTMLButtonElement>('.credential-remove')!.click())
    expect(calls).toContainEqual({ method: 'mcp.disconnect', payload: { serverName: 'fixture' } })
    expect(container.querySelector('.credential-remove')).toBeNull()
  })

  it('shows safe failures and permits retry', async () => {
    rejectConnect = true
    await renderDialog()
    await connectServer()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('2026-07-28')
    expect(container.textContent).not.toContain('private endpoint details')
    expect(container.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(false)
    rejectConnect = false
    await connectServer()
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })

  it('keeps in-flight requests alive across parent renders and aborts on unmount', async () => {
    await renderDialog()
    const capture = vi.mocked(fetch).mock.calls[0]![1]!.signal!
    await renderDialog()
    expect(capture.aborted).toBe(false)
    act(() => mounted.unmount())
    expect(capture.aborted).toBe(true)
    mounted = createRoot(container)
  })
})
