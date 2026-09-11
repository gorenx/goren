import { useEffect, useRef, useState } from 'react'
import type { HarnessAPI } from '../api'
import { useI18n } from '../i18n'
import type { CredentialsDescribeValue, CredentialView } from '../types'

// The saved Provider reference selects a write-only credential slot. Draft
// Provider edits cannot redirect a secret before their configuration is saved.
export function SettingsCredential({ api, reference, onChanged }: { api: HarnessAPI; reference: string; onChanged?: () => Promise<void> }): React.JSX.Element {
  const { translate } = useI18n()
  const [metadata, setMetadata] = useState<CredentialView>()
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState(false)
  const [saved, setSaved] = useState(false)
  const pending = useRef(false)
  const lifetime = useRef<AbortController | undefined>(undefined)

  useEffect(() => {
    const controller = new AbortController()
    lifetime.current = controller
    void api.call<CredentialsDescribeValue>('credentials.describe', { refs: [reference] }, controller.signal).then(result => {
      if (!controller.signal.aborted) setMetadata(result.credentials[reference])
    }).catch(() => { if (!controller.signal.aborted) setFailed(true) })
    return () => controller.abort()
  }, [api, reference])

  const perform = async (method: 'credentials.set' | 'credentials.unset' | 'credentials.describe'): Promise<void> => {
    if (pending.current) return
    pending.current = true
    setBusy(true)
    setFailed(false)
    setSaved(false)
    const signal = lifetime.current?.signal
    try {
      if (method !== 'credentials.describe') {
        await api.call(method, method === 'credentials.set' ? { ref: reference, value: draft.trim() } : { ref: reference }, signal)
        setDraft('')
        await onChanged?.()
      }
      const result = await api.call<CredentialsDescribeValue>('credentials.describe', { refs: [reference] }, signal)
      if (!signal?.aborted) {
        setMetadata(result.credentials[reference])
        setSaved(method !== 'credentials.describe')
      }
    } catch {
      if (!signal?.aborted) setFailed(true)
    } finally {
      pending.current = false
      if (!signal?.aborted) setBusy(false)
    }
  }

  return <section className="settings-credential" aria-labelledby="settings-key-title">
    <h3 id="settings-key-title">API Key</h3>
    <p className="settings-help">{translate('settings.keyHint', { reference })}</p>
    {metadata !== undefined && <p className="settings-help">{metadata.configured ? translate('details.configured', { source: metadata.source ?? 'unknown' }) : translate('details.notConfigured')}</p>}
    {metadata?.writable === false && <p className="settings-help">{translate('settings.keyReadOnly')}</p>}
    {metadata?.writable && <form onSubmit={event => { event.preventDefault(); void perform('credentials.set') }}>
      <label htmlFor="settings-api-key">{translate('credential.newKey')}</label>
      <input id="settings-api-key" className="credential-input" type="password" autoComplete="new-password" value={draft} disabled={busy} onChange={event => { setDraft(event.target.value); setSaved(false) }} />
      <div className="settings-actions">
        {metadata.configured && <button type="button" className="credential-remove" disabled={busy} onClick={() => void perform('credentials.unset')}>{translate('credential.remove')}</button>}
        <button type="submit" className="credential-save" disabled={busy || draft.trim() === ''}>{translate('settings.saveKey')}</button>
      </div>
    </form>}
    {failed && <div role="alert"><p>{translate('settings.keyFailure')}</p><button type="button" className="credential-cancel" disabled={busy} onClick={() => void perform('credentials.describe')}>{translate('settings.reload')}</button></div>}
    {saved && <p role="status" className="settings-help">{translate('settings.keySaved')}</p>}
  </section>
}
