import { useState } from 'react'
import type { ConversationStore } from '../conversation-store'
import type { PendingQuestionRequest, QuestionAnswerItem, QuestionItem } from '../types'

interface QuestionCardProps {
  store: ConversationStore
  request: PendingQuestionRequest
}

interface QuestionDraft {
  selected: string[]
  custom: string
}

export function QuestionCard({ store, request }: QuestionCardProps): React.JSX.Element {
  const [drafts, setDrafts] = useState(() => new Map<string, QuestionDraft>(
    request.questions.map(question => [question.id, { selected: [], custom: '' }]),
  ))
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string>()

  const updateDraft = (question: QuestionItem, nextDraft: QuestionDraft): void => {
    const nextDrafts = new Map(drafts)
    nextDrafts.set(question.id, nextDraft)
    setDrafts(nextDrafts)
    setFailure(undefined)
  }

  const choose = (question: QuestionItem, label: string): void => {
    const current: QuestionDraft = drafts.get(question.id) ?? { selected: [], custom: '' }
    if (question.multiSelect === true) {
      const selected = current.selected.includes(label)
        ? current.selected.filter(candidate => candidate !== label)
        : [...current.selected, label]
      updateDraft(question, { ...current, selected })
      return
    }
    updateDraft(question, { selected: [label], custom: '' })
  }

  const writeCustom = (question: QuestionItem, custom: string): void => {
    const current: QuestionDraft = drafts.get(question.id) ?? { selected: [], custom: '' }
    updateDraft(question, {
      selected: question.multiSelect === true ? current.selected : [],
      custom,
    })
  }

  const answers = (): QuestionAnswerItem[] | undefined => {
    const items: QuestionAnswerItem[] = []
    for (const question of request.questions) {
      const draft: QuestionDraft = drafts.get(question.id) ?? { selected: [], custom: '' }
      const custom = draft.custom.trim()
      if (draft.selected.length === 0 && custom === '') return undefined
      items.push({
        id: question.id,
        selected: draft.selected,
        ...(custom === '' ? {} : { custom }),
      })
    }
    return items
  }

  const submit = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    const answerItems = answers()
    if (answerItems === undefined) {
      setFailure('请回答每一个问题')
      return
    }
    setBusy(true)
    setFailure(undefined)
    try {
      await store.answerQuestion(request, answerItems)
    } catch {
      setBusy(false)
    }
  }

  const cancel = async (): Promise<void> => {
    setBusy(true)
    setFailure(undefined)
    try {
      await store.cancelQuestion(request)
    } catch {
      setBusy(false)
    }
  }

  return (
    <aside className="question-dock" data-question-rpc-id={request.rpcId} aria-label="Agent 正在等待回答">
      <form className="question-card" onSubmit={event => void submit(event)}>
        <div className="question-card-header">
          <div>
            <p className="question-eyebrow">AGENT NEEDS INPUT</p>
            <h2>回答后继续当前任务</h2>
          </div>
          <span className="question-waiting"><span aria-hidden="true" />WAITING</span>
        </div>

        <div className="question-list">
          {request.questions.map((question, index) => {
            const draft: QuestionDraft = drafts.get(question.id) ?? { selected: [], custom: '' }
            const options = question.options ?? []
            return (
              <fieldset className="question-field" key={question.id} disabled={busy}>
                <legend>
                  <span>{question.header ?? `问题 ${String(index + 1)}`}</span>
                  <strong>{question.question}</strong>
                </legend>
                {question.detail !== undefined && <p className="question-detail">{question.detail}</p>}
                {options.length !== 0 && (
                  <div className="question-options">
                    {options.map(option => {
                      const selected = draft.selected.includes(option.label)
                      return (
                        <button
                          type="button"
                          className={`question-option${selected ? ' is-selected' : ''}`}
                          key={option.label}
                          data-question-option={option.label}
                          aria-pressed={selected}
                          onClick={() => choose(question, option.label)}
                        >
                          <span className="question-option-mark" aria-hidden="true">{selected ? '✓' : ''}</span>
                          <span>
                            <strong>{option.label}</strong>
                            {option.description !== undefined && <small>{option.description}</small>}
                          </span>
                        </button>
                      )
                    })}
                  </div>
                )}
                <label className="question-custom">
                  <span>{options.length === 0 ? '你的回答' : '其他回答'}</span>
                  <textarea
                    rows={2}
                    value={draft.custom}
                    placeholder={options.length === 0 ? '输入回答…' : '也可以直接输入…'}
                    onChange={event => writeCustom(question, event.target.value)}
                  />
                </label>
              </fieldset>
            )
          })}
        </div>

        <div className="question-actions">
          <div className="min-w-0 flex-1">{failure !== undefined && <p role="alert">{failure}</p>}</div>
          <button type="button" className="question-cancel" disabled={busy} onClick={() => void cancel()}>取消提问</button>
          <button type="submit" className="question-submit" data-question-submit disabled={busy}>{busy ? '正在继续…' : '回答并继续'}</button>
        </div>
      </form>
    </aside>
  )
}
