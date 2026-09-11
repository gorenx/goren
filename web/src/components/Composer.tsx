import { useLayoutEffect, useRef, useState } from 'react'
import type { ConversationSnapshot } from '../types'
import type { ConversationStore } from '../conversation-store'
import { encodeImage, imageTypes, type ImageInput } from '../images'
import { ModelPicker } from './ModelPicker'
import { WorkspacePicker } from './WorkspacePicker'
import { SendIcon, StopIcon } from '../icons'
import { useI18n } from '../i18n'

interface ComposerProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
}

export function Composer({ store, snapshot }: ComposerProps): React.JSX.Element {
  const { translate } = useI18n()
  const [draft, setDraft] = useState('')
  const [pictures, setPictures] = useState<ImageInput[]>([])
  const [reading, setReading] = useState(false)
  const [imageError, setImageError] = useState(false)
  const picker = useRef<HTMLInputElement>(null)
  const [cancelling, setCancelling] = useState(false)
  const textarea = useRef<HTMLTextAreaElement>(null)
  const composing = useRef(false)
  const submitting = useRef(false)
  const waitingForAnswer = store.currentQuestion() !== undefined
  const agentRunning = store.currentSession()?.running === true
  const canCompose = snapshot.currentSessionId !== undefined && snapshot.phase !== 'failed' && !waitingForAnswer && !snapshot.creatingSession

  useLayoutEffect(() => {
    const element = textarea.current
    if (element === null) return
    element.style.height = 'auto'
    element.style.height = `${String(Math.min(element.scrollHeight, 224))}px`
  }, [draft])

  const send = async (): Promise<void> => {
    const text = draft.trim()
    if ((text === '' && pictures.length === 0) || reading || !canCompose || composing.current || submitting.current) return
    submitting.current = true
    const batch = pictures
    setPictures([])
    setDraft('')
    try {
      await store.submitPrompt(text, batch)
    } catch {
      setPictures(current => [...batch, ...current])
      setDraft(currentDraft => currentDraft === '' ? text : currentDraft)
    } finally {
      submitting.current = false
    }
  }

  const choose = async (files: File[]): Promise<void> => {
    if (reading || submitting.current) return
    setImageError(false)
    if (pictures.length + files.length > 20 || files.some(file => !imageTypes.includes(file.type) || file.size === 0 || file.size > 20 * 1024 * 1024)) {
      setImageError(true)
      return
    }
    setReading(true)
    try {
      const batch = await Promise.all(files.map(encodeImage))
      setPictures(current => [...current, ...batch])
    } catch { setImageError(true) } finally { setReading(false) }
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
        {pictures.length > 0 && <div className="flex flex-wrap gap-2 px-3 pt-3">
          {pictures.map((picture, index) => <div key={index} className="relative rounded-lg border border-black/10 p-1">
            <img className="h-16 w-20 rounded object-cover" src={`data:${picture.mediaType};base64,${picture.data}`} alt={picture.name} />
            <button type="button" className="absolute right-0 top-0 rounded-full bg-white px-1 shadow" aria-label={`${translate('composer.removeImage')} ${picture.name}`} onClick={() => setPictures(current => current.filter((_, position) => position !== index))}>×</button>
          </div>)}
          <p className="w-full text-xs text-tertiary">{translate('composer.vision')}</p>
        </div>}
        {imageError && <p role="alert" className="px-3 pt-2 text-xs text-red-700">{translate('composer.imageError')}</p>}
        <input ref={picker} type="file" accept={imageTypes.join(',')} multiple hidden aria-label={translate('composer.addImage')} onChange={event => {
          const files = Array.from(event.currentTarget.files ?? [])
          event.currentTarget.value = ''
          void choose(files)
        }} />
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
            <button type="button" className="icon-button" disabled={!canCompose || reading} aria-label={translate('composer.addImage')} title={translate('composer.addImage')} onClick={() => picker.current?.click()}><span aria-hidden="true" className="text-xl">＋</span></button>
            {snapshot.currentSessionId !== undefined && <ModelPicker store={store} sessionId={snapshot.currentSessionId} />}
            <WorkspacePicker store={store} cwd={store.currentSession()?.cwd ?? snapshot.host?.cwd} disabled={snapshot.creatingSession || snapshot.phase !== 'ready' || reading || submitting.current} />
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
              <button id="send" type="submit" className="send-button" disabled={!canCompose || reading || (draft.trim() === '' && pictures.length === 0)} aria-label={translate('composer.send')}>
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
