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
  events: new Map(),
  streams: new Map(),
  pendingQuestions: new Map(),
  localTitles: new Map(),
  currentSessionId: session.sessionId,
  creatingSession: false,
  onlineDownlinks: 2,
  composerState: 'composer.ready',
  credentialLoaded: true,
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

  it('submits the controlled textarea value once', async () => {
    const submitPrompt = vi.fn(async () => {})
    const fixture = renderComposer(submitPrompt)

    await act(async () => replaceTextareaValue(fixture.textarea, '  inspect this  '))
    expect(fixture.sendButton.disabled).toBe(false)
    await act(async () => fixture.sendButton.click())

    expect(submitPrompt).toHaveBeenCalledTimes(1)
    expect(submitPrompt).toHaveBeenCalledWith('inspect this')
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

  function renderComposer(
    submitPrompt: (text: string) => Promise<void>,
  ): ComposerFixture {
    const store = {
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
