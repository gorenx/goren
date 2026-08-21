import { useLayoutEffect, useRef, useState } from 'react'
import type { ConversationSnapshot } from '../types'
import type { ConversationStore } from '../conversation-store'
import { SendIcon, SparkIcon, StopIcon } from '../icons'
import { useI18n } from '../i18n'

interface ComposerProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
}

export function Composer({ store, snapshot }: ComposerProps): React.JSX.Element {
  const { translate } = useI18n()
  const [draft, setDraft] = useState('')
  const [cancelling, setCancelling] = useState(false)
  const textarea = useRef<HTMLTextAreaElement>(null)
  const composing = useRef(false)
  const submitting = useRef(false)
  const waitingForAnswer = store.currentQuestion() !== undefined
  const agentRunning = store.currentSession()?.running === true
  const canCompose = snapshot.currentSessionId !== undefined && snapshot.phase !== 'failed' && !waitingForAnswer

  useLayoutEffect(() => {
    const element = textarea.current
    if (element === null) return
    element.style.height = 'auto'
    element.style.height = `${String(Math.min(element.scrollHeight, 224))}px`
  }, [draft])

  const send = async (): Promise<void> => {
    const text = draft.trim()
    if (text === '' || !canCompose || composing.current || submitting.current) return
    submitting.current = true
    setDraft('')
    try {
      await store.submitPrompt(text)
    } catch {
      setDraft(currentDraft => currentDraft === '' ? text : currentDraft)
    } finally {
      submitting.current = false
    }
  }

  const stop = async (): Promise<void> => {
    if (!agentRunning || cancelling) return
    setCancelling(true)
    try {
      await store.cancelCurrentTurn()
    } catch {
      // ConversationStore owns the user-visible failure state.
    } finally {
      setCancelling(false)
    }
  }

  return (
    <div className="composer-seat">
      <form
        id="composer"
        className="composer"
        onSubmit={event => {
          event.preventDefault()
          void send()
        }}
      >
        <label className="sr-only" htmlFor="prompt">{translate('composer.label')}</label>
        <textarea
          ref={textarea}
          id="prompt"
          rows={1}
          disabled={!canCompose}
          value={draft}
          placeholder={waitingForAnswer ? translate('composer.waitingPlaceholder') : translate('composer.placeholder')}
          autoComplete="off"
          className="composer-input"
          onChange={event => setDraft(event.currentTarget.value)}
          onCompositionStart={() => { composing.current = true }}
          onCompositionEnd={() => { composing.current = false }}
          onKeyDown={event => {
            const compositionActive = composing.current || event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229
            if (event.key === 'Enter' && !event.shiftKey && !compositionActive) {
              event.preventDefault()
              void send()
            }
          }}
        />
        <div className="flex items-center justify-between gap-3 px-3 pb-2.5">
          <div className="flex min-w-0 items-center gap-2 text-xs text-tertiary">
            <span className="inline-flex items-center gap-1.5 rounded-lg px-2 py-1 hover:bg-black/[0.035]">
              <SparkIcon size={14} />
              <span className="max-w-40 truncate">{snapshot.host?.model ?? 'DeepSeek'}</span>
            </span>
            <span className="hidden font-mono text-[9px] uppercase tracking-[0.08em] text-caption sm:inline">{translate('composer.enterToSend')}</span>
          </div>
          {agentRunning
            ? (
              <button
                id="stop-agent"
                type="button"
                className="stop-button"
                disabled={cancelling}
                aria-label={translate('composer.stop')}
                title={translate('composer.stop')}
                onClick={() => void stop()}
              >
                <StopIcon size={16} />
              </button>
              )
            : (
              <button id="send" type="submit" className="send-button" disabled={!canCompose || draft.trim() === ''} aria-label={translate('composer.send')}>
                <SendIcon size={18} />
              </button>
              )}
        </div>
      </form>
      <div id="composer-state" className="mt-2 flex items-center justify-center gap-2 font-mono text-[9px] tracking-[0.08em] text-caption">
        <span className={snapshot.currentSessionId !== undefined && store.currentSession()?.running ? 'status-pulse' : 'status-pin'} aria-hidden="true" />
        {translate(snapshot.composerState)}
      </div>
    </div>
  )
}
