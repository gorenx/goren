import { useLayoutEffect, useRef, useState } from 'react'
import type { ConversationSnapshot } from '../types'
import type { ConversationStore } from '../conversation-store'
import { SendIcon, SparkIcon } from '../icons'
import { useI18n } from '../i18n'

interface ComposerProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
}

export function Composer({ store, snapshot }: ComposerProps): React.JSX.Element {
  const { translate } = useI18n()
  const [draft, setDraft] = useState('')
  const textarea = useRef<HTMLTextAreaElement>(null)
  const waitingForAnswer = store.currentQuestion() !== undefined
  const canCompose = snapshot.currentSessionId !== undefined && snapshot.phase !== 'failed' && !waitingForAnswer

  useLayoutEffect(() => {
    const element = textarea.current
    if (element === null) return
    element.style.height = 'auto'
    element.style.height = `${String(Math.min(element.scrollHeight, 224))}px`
  }, [draft])

  const send = async (): Promise<void> => {
    const text = textarea.current?.value.trim() ?? ''
    if (text === '' || !canCompose) return
    setDraft('')
    if (textarea.current !== null) textarea.current.value = ''
    try {
      await store.submitPrompt(text)
    } catch {
      setDraft(text)
      if (textarea.current !== null) textarea.current.value = text
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
          defaultValue=""
          placeholder={waitingForAnswer ? translate('composer.waitingPlaceholder') : translate('composer.placeholder')}
          autoComplete="off"
          className="composer-input"
          onInput={event => setDraft(event.currentTarget.value)}
          onKeyDown={event => {
            if (event.key === 'Enter' && !event.shiftKey) {
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
          <button id="send" type="submit" className="send-button" disabled={!canCompose || draft.trim() === ''} aria-label={translate('composer.send')}>
            <SendIcon size={18} />
          </button>
        </div>
      </form>
      <div id="composer-state" className="mt-2 flex items-center justify-center gap-2 font-mono text-[9px] tracking-[0.08em] text-caption">
        <span className={snapshot.currentSessionId !== undefined && store.currentSession()?.running ? 'status-pulse' : 'status-pin'} aria-hidden="true" />
        {translate(snapshot.composerState)}
      </div>
    </div>
  )
}
