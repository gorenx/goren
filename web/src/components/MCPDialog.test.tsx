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
  let rejectRetry: boolean

  beforeEach(() => {
    container = document.createElement('div')
    document.body.append(container)
    mounted = createRoot(container)
    calls = []
    connections = [{ serverName: 'fixture', transport: 'streamable-http', status: 'failed', toolCount: 0 }]
    rejectRetry = false
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
      const body = JSON.parse(String(init.body)) as { rpcId: string; method: string; payload: unknown }
      calls.push({ method: body.method, payload: body.payload })
      let value: unknown = connections
      if (body.method === 'mcp.retry') {
        if (rejectRetry) return Response.json({ rpcId: body.rpcId, result: { ok: false, error: { message: 'private endpoint details' } } })
        const request = body.payload as { serverName: string }
        value = { serverName: request.serverName, transport: 'streamable-http', status: 'connected', toolCount: 2 }
        connections = [value as object]
      }
      return Response.json({ rpcId: body.rpcId, result: { ok: true, value } })
    }))
  })

  afterEach(() => {
    act(() => mounted.unmount())
    vi.unstubAllGlobals()
  })

  async function renderDialog(): Promise<void> {
    await act(async () => mounted.render(<I18nProvider><MCPDialog sessionId="agent" onClose={() => {}} onOpenSettings={() => {}} /></I18nProvider>))
  }

  async function retryServer(): Promise<void> {
    await act(async () => container.querySelector<HTMLButtonElement>('.credential-remove')!.click())
  }

  it('queries and retries only the selected session without temporary configuration', async () => {
    await renderDialog()
    expect(calls).toContainEqual({ method: 'mcp.list', payload: { sessionId: 'agent' } })
    await retryServer()
    expect(calls).toContainEqual({ method: 'mcp.retry', payload: { sessionId: 'agent', serverName: 'fixture' } })
    expect(container.textContent).toContain('fixture')
    expect(container.textContent).toContain('2')
    expect(container.querySelector('form')).toBeNull()
    expect(calls.some(call => call.method === 'mcp.connect' || call.method === 'mcp.disconnect')).toBe(false)
  })

  it('shows safe failures and permits retry', async () => {
    rejectRetry = true
    await renderDialog()
    await retryServer()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('2026-07-28')
    expect(container.textContent).not.toContain('private endpoint details')
    expect(container.querySelector<HTMLButtonElement>('.credential-remove')!.disabled).toBe(false)
    rejectRetry = false
    await retryServer()
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
