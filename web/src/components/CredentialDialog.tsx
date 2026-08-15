import { useEffect, useRef, useState } from 'react'
import type { ConversationSnapshot } from '../types'
import type { ConversationStore } from '../conversation-store'
import { CloseIcon, KeyIcon } from '../icons'
import { useI18n } from '../i18n'

interface CredentialDialogProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
  onClose: () => void
}

const legalAPIKey = /^[\x21-\x7E]+$/
const environmentLine = /^[A-Z][A-Z0-9_]*=[^=]/

export function CredentialDialog({ store, snapshot, onClose }: CredentialDialogProps): React.JSX.Element {
  const { translate } = useI18n()
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string>()
  const input = useRef<HTMLInputElement>(null)
  const credential = snapshot.credential

  useEffect(() => input.current?.focus(), [])

  const save = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    const value = draft.trim()
    if (value === '') {
      setFailure(translate('credential.required'))
      return
    }
    if (!legalAPIKey.test(value) || environmentLine.test(value) || isQuoted(value)) {
      setFailure(translate('credential.invalid'))
      return
    }
    setBusy(true)
    setFailure(undefined)
    try {
      await store.saveCredential(value)
      setDraft('')
      onClose()
    } catch {
      setBusy(false)
    }
  }

  const remove = async (): Promise<void> => {
    setBusy(true)
    setFailure(undefined)
    try {
      await store.unsetCredential()
      setDraft('')
      setBusy(false)
    } catch {
      setBusy(false)
    }
  }

  return (
    <div className="credential-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}>
      <section className="credential-dialog" role="dialog" aria-modal="true" aria-labelledby="credential-title">
        <header className="flex items-start gap-3 border-b border-black/[0.06] px-5 py-4">
          <span className="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-brand-soft text-brand"><KeyIcon size={18} /></span>
          <div className="min-w-0 flex-1">
            <h2 id="credential-title" className="font-display text-base font-semibold text-ink">DeepSeek API Key</h2>
            <p className="mt-1 text-xs leading-5 text-tertiary">{translate('credential.description')}</p>
          </div>
          <button type="button" className="icon-button" aria-label={translate('credential.close')} onClick={onClose}><CloseIcon size={16} /></button>
        </header>

        <form className="p-5" onSubmit={event => void save(event)}>
          <div className="mb-4 flex items-center justify-between rounded-xl bg-black/[0.025] px-3 py-2.5 text-xs">
            <span className="text-tertiary">{translate('credential.currentStatus')}</span>
            <span className={credential?.configured ? 'font-medium text-deepseek' : 'font-medium text-secondary'}>
              {credential?.configured ? translate('details.configured', { source: credential.source ?? 'unknown' }) : translate('details.notConfigured')}
            </span>
          </div>

          {credential?.writable === false
            ? <p className="rounded-xl border border-black/[0.07] bg-sidebar px-3 py-3 text-xs leading-5 text-secondary">{translate('credential.environmentReadOnly')} <code className="font-mono text-[11px]">DEEPSEEK_API_KEY</code>{translate('credential.environmentSuffix')}</p>
            : (
              <>
                <label className="mb-2 block text-xs font-medium text-secondary" htmlFor="deepseek-api-key">{translate('credential.newKey')}</label>
                <input
                  ref={input}
                  id="deepseek-api-key"
                  type="password"
                  autoComplete="new-password"
                  className="credential-input"
                  placeholder={credential?.configured ? translate('credential.replacePlaceholder') : 'sk-…'}
                  value={draft}
                  disabled={busy}
                  onChange={event => { setDraft(event.target.value); setFailure(undefined) }}
                />
                {failure !== undefined && <p className="mt-2 text-xs text-red-600">{failure}</p>}
                <div className="mt-5 flex items-center justify-between gap-3">
                  <div>
                    {credential?.configured && <button type="button" className="credential-remove" disabled={busy} onClick={() => void remove()}>{translate('credential.remove')}</button>}
                  </div>
                  <div className="flex items-center gap-2">
                    <button type="button" className="credential-cancel" disabled={busy} onClick={onClose}>{translate('credential.later')}</button>
                    <button type="submit" className="credential-save" disabled={busy || draft.length === 0}>{busy ? translate('credential.saving') : translate('credential.save')}</button>
                  </div>
                </div>
              </>
            )}
        </form>
      </section>
    </div>
  )
}

function isQuoted(value: string): boolean {
  const first = value[0]
  return (first === '"' || first === "'" || first === '`') && value.length > 1 && value.endsWith(first)
}
