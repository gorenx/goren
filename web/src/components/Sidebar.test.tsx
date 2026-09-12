import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ConversationStore } from '../conversation-store'
import { I18nProvider } from '../i18n'
import type { SessionSummary } from '../types'
import { Sidebar } from './Sidebar'

const cleanups: Array<() => void> = []
afterEach(() => { for (const cleanup of cleanups.splice(0)) cleanup() })

it('hides child sessions until expanded and opens their output with live status', async () => {
  localStorage.setItem('goren.locale', 'en')
  const store = new ConversationStore(key => key)
  const select = vi.spyOn(store, 'selectSession').mockResolvedValue()
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  cleanups.push(() => { act(() => root.unmount()); host.remove(); store.dispose() })
  const parent: SessionSummary = { sessionId: 'parent', updatedAt: 1, running: false, blank: false }
  const child: SessionSummary = { ...parent, sessionId: 'child', parentSessionId: 'parent', origin: 'subagent', running: true }
  const grandchild: SessionSummary = { ...parent, sessionId: 'grandchild', parentSessionId: 'child' }
  const render = (running: boolean): void => act(() => root.render(<I18nProvider><Sidebar store={store} snapshot={{ ...store.snapshot(), sessions: [grandchild, { ...child, running }, parent] }} collapsed={false} onToggle={() => {}} onOpenCompanions={() => {}} onOpenMCP={() => {}} onOpenSettings={() => {}} /></I18nProvider>))
  render(true)
  expect(host.querySelector('[data-session-id="child"]')).toBeNull()
  const toggle = host.querySelector<HTMLButtonElement>('.session-branch-toggle')!
  expect(toggle.getAttribute('aria-expanded')).toBe('false')
  act(() => toggle.click())
  const button = host.querySelector<HTMLButtonElement>('[data-session-id="child"]')!
  expect(button).not.toBeNull()
  expect(button.closest('.is-child')?.querySelector('.session-branch-line')).not.toBeNull()
  expect(button.querySelector('.session-status-dot.is-running')).not.toBeNull()
  expect(host.querySelector('[data-session-id="grandchild"]')).toBeNull()
  await act(async () => button.click())
  expect(select).toHaveBeenCalledWith('child')
  render(false)
  expect(host.querySelector('[data-session-id="child"] .session-status-dot')?.getAttribute('aria-label')).toBe('Not running')
  act(() => toggle.click())
  expect(host.querySelector('[data-session-id="child"]')).toBeNull()
})
