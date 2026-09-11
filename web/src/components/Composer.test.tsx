import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConversationStore } from '../conversation-store'
import { I18nProvider } from '../i18n'
import type { ConversationSnapshot, SessionSummary } from '../types'
import { replaceTextareaValue } from '../test/dom'
import { Composer } from './Composer'

const session: SessionSummary = {
  sessionId: 'session-1',
  updatedAt: 1,
  running: false,
  blank: true,
}

const snapshot: ConversationSnapshot = {
  phase: 'ready',
	sessions: [session],
	loadingMoreSessions: false,
  events: new Map(),
  histories: new Map(),
  streams: new Map(),
  pendingQuestions: new Map(),
  localTitles: new Map(),
  currentSessionId: session.sessionId,
  creatingSession: false,
  onlineDownlinks: 2,
  composerState: 'composer.ready',
	credentialLoaded: true,
	boundDefinitionsLoaded: true,
	boundDefinitions: [],
	boundToolsState: 'ready',
	boundTools: [],
	boundExtensionsState: 'ready',
	boundExtensions: [],
}

interface ComposerFixture {
  root: Root
  textarea: HTMLTextAreaElement
  sendButton: HTMLButtonElement
}

describe('Composer', () => {
  let container: HTMLDivElement
  let mountedRoot: Root | undefined

  beforeEach(() => {
    container = document.createElement('div')
    document.body.append(container)
  })

  afterEach(() => {
    if (mountedRoot !== undefined) {
      act(() => mountedRoot?.unmount())
      mountedRoot = undefined
    }
  })

  it.each(['cancel', 'unmount'])('opens one native picker and preserves the chat draft on %s', async action => {
    let finish: (() => void) | undefined
    const pickWorkspace = vi.fn(() => new Promise<void>(resolve => { finish = resolve }))
    const submitPrompt = vi.fn(async () => {})
    const store = {
      sessionModels: async () => ({ current: { provider: 'deepseek', model: 'deepseek-v4-flash' }, groups: [] }),
      currentQuestion: () => undefined,
      currentSession: () => ({ ...session, cwd: '/workspace-a' }),
      pickWorkspace,
      submitPrompt,
    } as unknown as ConversationStore
    act(() => {
      mountedRoot = createRoot(container)
      mountedRoot.render(<I18nProvider><Composer store={store} snapshot={snapshot} /></I18nProvider>)
    })
    const textarea = container.querySelector<HTMLTextAreaElement>('#prompt')!
    await act(async () => replaceTextareaValue(textarea, 'keep this draft'))
    const trigger = container.querySelector<HTMLButtonElement>('.workspace-button')!
    await act(async () => { trigger.click(); trigger.click() })
    expect(pickWorkspace).toHaveBeenCalledTimes(1)
    expect(trigger.disabled).toBe(true)
    expect(document.querySelector('[role=dialog]')).toBeNull()
    expect(document.querySelector('#workspace-path')).toBeNull()
    expect(submitPrompt).not.toHaveBeenCalled()
    expect(textarea.value).toBe('keep this draft')
    const signal = (pickWorkspace.mock.calls[0] as unknown as [AbortSignal])[0]
    if (action === 'cancel') {
      const cancel = [...container.querySelectorAll<HTMLButtonElement>('button')].find(button => /Cancel directory selection|取消目录选择/.test(button.getAttribute('aria-label') ?? ''))!
      await act(async () => cancel.click())
      expect(textarea.value).toBe('keep this draft')
    } else {
      act(() => mountedRoot?.unmount())
      mountedRoot = undefined
    }
    expect(signal.aborted).toBe(true)
    await act(async () => finish?.())
    if (action === 'cancel') expect(trigger.disabled).toBe(false)
  })

  it('sends cancellation from the stop button and waits for idle before restoring send', async () => {
    let running = true
    const cancelCurrentTurn = vi.fn(async () => {})
    const submitPrompt = vi.fn(async () => {})
    const store = {
      sessionModels: async () => ({ current: { provider: 'deepseek', model: 'deepseek-v4-flash' }, groups: [] }),
      currentQuestion: () => undefined,
      currentSession: () => ({ ...session, running }),
      submitPrompt,
      cancelCurrentTurn,
    } as unknown as ConversationStore
    const render = () => mountedRoot!.render(
      <I18nProvider><Composer store={store} snapshot={snapshot} /></I18nProvider>,
    )
    act(() => { mountedRoot = createRoot(container); render() })
    const button = container.querySelector<HTMLButtonElement>('#stop-agent')!
    expect(button).not.toBeNull()
    await act(async () => button.click())
    expect(cancelCurrentTurn).toHaveBeenCalledTimes(1)
    expect(submitPrompt).not.toHaveBeenCalled()
    expect(container.querySelector('#stop-agent')).not.toBeNull()
    running = false
    act(render)
    expect(container.querySelector('#stop-agent')).toBeNull()
    expect(container.querySelector('#send')).not.toBeNull()
  })

  it('submits the controlled textarea value once', async () => {
    const submitPrompt = vi.fn(async () => {})
    const fixture = renderComposer(submitPrompt)

    await act(async () => replaceTextareaValue(fixture.textarea, '  inspect this  '))
    expect(fixture.sendButton.disabled).toBe(false)
    await act(async () => fixture.sendButton.click())

    expect(submitPrompt).toHaveBeenCalledTimes(1)
    expect(submitPrompt).toHaveBeenCalledWith('inspect this', [])
    expect(fixture.textarea.value).toBe('')
  })

  it('does not submit twice while the first prompt is pending', async () => {
    let finishSubmission: (() => void) | undefined
    const submitPrompt = vi.fn(() => new Promise<void>(resolve => {
      finishSubmission = resolve
    }))
    const fixture = renderComposer(submitPrompt)

    await act(async () => replaceTextareaValue(fixture.textarea, 'one prompt'))
    await act(async () => {
      fixture.textarea.dispatchEvent(enterKey())
      fixture.textarea.dispatchEvent(enterKey())
    })
    expect(submitPrompt).toHaveBeenCalledTimes(1)

    await act(async () => finishSubmission?.())
  })

  it('keeps Enter inside an active IME composition', async () => {
    const submitPrompt = vi.fn(async () => {})
    const fixture = renderComposer(submitPrompt)

    await act(async () => replaceTextareaValue(fixture.textarea, '输入内容'))
    await act(async () => {
      fixture.textarea.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
      fixture.textarea.dispatchEvent(enterKey())
    })
    expect(submitPrompt).not.toHaveBeenCalled()

    await act(async () => {
      fixture.textarea.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true }))
      fixture.textarea.dispatchEvent(enterKey())
    })
    expect(submitPrompt).toHaveBeenCalledTimes(1)
  })

  it('previews an image-only prompt and retains it when submission fails', async () => {
    const submitPrompt = vi.fn(async () => { throw new Error('choose vision') })
    const fixture = renderComposer(submitPrompt)
    const picker = container.querySelector<HTMLInputElement>('input[type=file]')!
    Object.defineProperty(picker, 'files', { value: [new File(['image bytes'], 'picture.png', { type: 'image/png' })], configurable: true })
    await act(async () => {
      picker.dispatchEvent(new Event('change', { bubbles: true }))
      await new Promise(resolve => setTimeout(resolve, 30))
    })
    expect(container.querySelector('img')?.alt).toBe('picture.png')
    expect(fixture.sendButton.disabled).toBe(false)
    await act(async () => fixture.sendButton.click())
    expect(submitPrompt).toHaveBeenCalledWith('', [expect.objectContaining({ type: 'image', name: 'picture.png', data: btoa('image bytes') })])
    expect(container.querySelector('img')?.alt).toBe('picture.png')
    const remove = container.querySelector<HTMLButtonElement>('button[aria-label$="picture.png"]')!
    await act(async () => remove.click())
    expect(container.querySelector('img')).toBeNull()
    expect(fixture.sendButton.disabled).toBe(true)
  })

  it('rejects unsupported files without submitting', async () => {
    const submitPrompt = vi.fn(async () => {})
    renderComposer(submitPrompt)
    const picker = container.querySelector<HTMLInputElement>('input[type=file]')!
    Object.defineProperty(picker, 'files', { value: [new File(['document'], 'note.txt', { type: 'text/plain' })] })
    await act(async () => picker.dispatchEvent(new Event('change', { bubbles: true })))
    expect(container.querySelector('[role=alert]')).not.toBeNull()
    expect(submitPrompt).not.toHaveBeenCalled()
  })

  function renderComposer(
    submitPrompt: (text: string) => Promise<void>,
  ): ComposerFixture {
    const store = {
      sessionModels: async () => ({ current: { provider: 'deepseek', model: 'deepseek-v4-flash' }, groups: [] }),
      currentQuestion: () => undefined,
      currentSession: () => session,
      submitPrompt,
      cancelCurrentTurn: async () => {},
    } as unknown as ConversationStore
    act(() => {
      mountedRoot = createRoot(container)
      mountedRoot.render(
        <I18nProvider>
          <Composer store={store} snapshot={snapshot} />
        </I18nProvider>,
      )
    })
    const textarea = document.getElementById('prompt')
    const sendButton = document.getElementById('send')
    if (!(textarea instanceof HTMLTextAreaElement) ||
        !(sendButton instanceof HTMLButtonElement) ||
        mountedRoot === undefined) {
      throw new Error('web test: Composer controls are missing')
    }
    return {
      root: mountedRoot,
      textarea,
      sendButton,
    }
  }
})

function enterKey(): KeyboardEvent {
  return new KeyboardEvent('keydown', {
    key: 'Enter',
    code: 'Enter',
    keyCode: 13,
    bubbles: true,
  })
}
