import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConversationStore } from '../conversation-store'
import { I18nProvider } from '../i18n'
import type { PendingQuestionRequest, QuestionAnswerItem } from '../types'
import { replaceTextareaValue } from '../test/dom'
import { QuestionCard } from './QuestionCard'

const request: PendingQuestionRequest = {
  rpcId: 'question-1',
  sessionId: 'session-1',
  questions: [
    {
      id: 'mode',
      header: 'Mode',
      question: 'Choose a mode',
      options: [
        { label: 'Fast' },
        { label: 'Careful' },
      ],
    },
    {
      id: 'detail',
      question: 'Add details',
    },
  ],
}

describe('QuestionCard', () => {
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

  it('requires an answer for every question', async () => {
    const answerQuestion = vi.fn(async () => {})
    renderQuestion(answerQuestion)

    await act(async () => submitButton().click())

    expect(answerQuestion).not.toHaveBeenCalled()
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('Answer every question')
  })

  it('submits selected and custom answers with their question identities', async () => {
    const answerQuestion = vi.fn(async (_request: PendingQuestionRequest, _answers: QuestionAnswerItem[]) => {})
    renderQuestion(answerQuestion)
    const option = document.querySelector('[data-question-option="Careful"]')
    const textareas = document.querySelectorAll('textarea')
    if (!(option instanceof HTMLButtonElement) || textareas[1] === undefined) {
      throw new Error('web test: question controls are missing')
    }

    await act(async () => option.click())
    await act(async () => replaceTextareaValue(textareas[1], '  inspect races  '))
    await act(async () => submitButton().click())

    expect(answerQuestion).toHaveBeenCalledWith(request, [
      {
        id: 'mode',
        selected: ['Careful'],
      },
      {
        id: 'detail',
        selected: [],
        custom: 'inspect races',
      },
    ])
  })

  function renderQuestion(
    answerQuestion: (requestValue: PendingQuestionRequest, answers: QuestionAnswerItem[]) => Promise<void>,
  ): void {
    const store = {
      answerQuestion,
      cancelQuestion: async () => {},
    } as unknown as ConversationStore
    act(() => {
      mountedRoot = createRoot(container)
      mountedRoot.render(
        <I18nProvider>
          <QuestionCard store={store} request={request} />
        </I18nProvider>,
      )
    })
  }
})

function submitButton(): HTMLButtonElement {
  const button = document.querySelector('[data-question-submit]')
  if (!(button instanceof HTMLButtonElement)) throw new Error('web test: question submit is missing')
  return button
}
