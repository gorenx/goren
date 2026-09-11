import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { I18nProvider } from '../i18n'
import type { ConversationStore } from '../conversation-store'
import { ModelPicker } from './ModelPicker'

it('changes the model only after an explicit selection and retains selection on failure', async () => {
  const selectModel = vi.fn(async () => { throw new Error('selection failed') })
  const store = {
    sessionModels: async () => ({ current: { provider: 'deepseek', model: 'text' }, groups: [{ id: 'deepseek', name: 'DeepSeek', models: [{ id: 'text', name: 'Text' }, { id: 'vision', name: 'Vision' }] }] }),
    selectModel,
  } as unknown as ConversationStore
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  await act(async () => root.render(<I18nProvider><ModelPicker store={store} sessionId="session" /></I18nProvider>))
  expect(selectModel).not.toHaveBeenCalled()
  const select = container.querySelector('select')!
  await act(async () => {
    select.value = '1'
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  expect(selectModel).toHaveBeenCalledWith('session', 'deepseek', 'vision')
  expect(select.value).toBe('0')
  expect(container.querySelector('[role=alert]')?.textContent).toContain('selection failed')
  act(() => root.unmount())
})
