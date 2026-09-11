import { useEffect, useRef, useState } from 'react'
import { HarnessAPI, RPCError } from '../api'
import { useI18n } from '../i18n'
import { CloseIcon } from '../icons'
import { isRecord } from '../types'
import { decodeSettingsCatalog, decodeSettingsGroup, settingsIdentity, settingsLabels, settingsMethods, type SettingsCatalog, type SettingsGroup, type SettingsValue } from '../settings'
import { SettingsFields } from './SettingsFields'
import { SettingsCredential } from './SettingsCredential'

export function SettingsDialog({ onClose, onCredentialChanged }: { onClose: () => void; onCredentialChanged?: () => Promise<void> }): React.JSX.Element {
  const { translate } = useI18n()
  const translateRef = useRef(translate)
  translateRef.current = translate
  const [api] = useState(() => new HarnessAPI(() => {}, () => {}, () => {}, (key, values) => translateRef.current(key, values)))
  const [catalog, setCatalog] = useState<SettingsCatalog>()
  const [selected, setSelected] = useState<SettingsGroup>()
  const [draft, setDraft] = useState<SettingsValue>({})
  const [newName, setNewName] = useState('')
  const [creating, setCreating] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<'load' | 'save' | 'conflict'>()
  const [saved, setSaved] = useState(false)
  const pending = useRef(false)
  const lifetime = useRef<AbortController | undefined>(undefined)
  const dialog = useRef<HTMLElement>(null)
  const closeRef = useRef(onClose)
  closeRef.current = onClose

  const select = (group: SettingsGroup | undefined): void => {
    setSelected(group)
    setDraft(structuredClone(group?.value ?? (group?.ns === 'mcp' ? { enabled: true, connection: { serverName: group.name, transport: 'streamable-http', url: '' } } : {})))
    setDirty(false)
    setCreating(false)
    setSaved(false)
    setFailure(undefined)
  }

  useEffect(() => {
    const controller = new AbortController()
    lifetime.current = controller
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
    dialog.current?.focus()
    void api.call('settings.describe', {}, controller.signal).then(raw => {
      if (controller.signal.aborted) return
      const result = decodeSettingsCatalog(raw)
      setCatalog(result)
      select(result.namespaces[0])
    }).catch(() => { if (!controller.signal.aborted) setFailure('load') })
    return () => {
      controller.abort()
      previousFocus?.focus()
    }
  }, [api])

  const reload = async (): Promise<void> => {
    if (pending.current) return
    pending.current = true
    setBusy(true)
    const signal = lifetime.current?.signal
    try {
      const result = decodeSettingsCatalog(await api.call('settings.describe', {}, signal))
      if (signal?.aborted) return
      setCatalog(result)
      select(result.namespaces.find(group => selected !== undefined && settingsIdentity(group) === settingsIdentity(selected)) ?? result.namespaces[0])
    } catch {
      if (!signal?.aborted) setFailure('load')
    } finally {
      pending.current = false
      if (!signal?.aborted) setBusy(false)
    }
  }

  const write = async (operation: 'update' | 'reset' | 'retry'): Promise<void> => {
    if (selected === undefined || pending.current || !catalog?.writable) return
    pending.current = true
    setBusy(true)
    setFailure(undefined)
    setSaved(false)
    const signal = lifetime.current?.signal
    const name = creating ? newName.trim() : selected.name
    const complete = structuredClone(draft)
    if (selected.ns === 'prompt' && Array.isArray(complete.toolOrder)) complete.toolOrder = complete.toolOrder.filter(line => line !== '')
    if (selected.ns === 'llmprovider' && isRecord(complete.retryPolicy) && Array.isArray(complete.retryPolicy.retryableCodes)) complete.retryPolicy.retryableCodes = complete.retryPolicy.retryableCodes.filter(line => line !== '')
    const config = selected.ns === 'mcp' ? { ...draft, connection: { ...(draft.connection as SettingsValue), serverName: name, transport: 'streamable-http' } } : complete
    const method = operation === 'retry' ? 'settings.retryApplication' : settingsMethods[selected.ns][operation]
    const payload = operation === 'retry'
      ? { ns: selected.ns, name, expectedRevision: selected.revision }
      : operation === 'update' ? { name, expectedRevision: selected.revision, config } : { name, expectedRevision: selected.revision }
    try {
      const result = decodeSettingsGroup(await api.call(method, payload, signal))
      if (signal?.aborted) return
      if (result.ns !== selected.ns || result.name !== name) throw new Error('Wrong settings group')
      setCatalog(current => current === undefined ? current : { ...current, namespaces: [...current.namespaces.filter(group => settingsIdentity(group) !== settingsIdentity(result)), result] })
      select(result)
      setSaved(true)
    } catch (error) {
      if (!signal?.aborted) setFailure(error instanceof RPCError && error.code === 'settings-conflict' ? 'conflict' : 'save')
    } finally {
      pending.current = false
      if (!signal?.aborted) setBusy(false)
    }
  }

  const createMCP = (): void => {
    select({ ns: 'mcp', name: 'new', revision: 0, applies: 'live', value: { enabled: true, connection: { transport: 'streamable-http', url: '' } } })
    setCreating(true)
    setNewName('')
  }
  const change = (value: SettingsValue): void => { setDraft(value); setDirty(true); setSaved(false) }
  const keyReference = typeof selected?.value?.apiKeyEnv === 'string' ? selected.value.apiKeyEnv : 'DEEPSEEK_API_KEY'
  const canClose = !dirty && !busy
  const trapFocus = (event: React.KeyboardEvent): void => {
    if (event.key === 'Escape' && canClose) closeRef.current()
    if (event.key !== 'Tab') return
    const elements = [...(dialog.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), summary') ?? [])].filter(element => element.getClientRects().length > 0)
    const first = elements[0]
    const last = elements.at(-1)
    if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) { event.preventDefault(); last?.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
  }

  return <div className="credential-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && canClose) onClose() }}>
    <section ref={dialog} tabIndex={-1} onKeyDown={trapFocus} className="settings-dialog" role="dialog" aria-modal="true" aria-labelledby="settings-title">
      <header className="settings-header"><div><h2 id="settings-title">{translate('settings.title')}</h2><p className="settings-help">{translate('settings.scope')}</p></div><button type="button" className="icon-button" disabled={!canClose} aria-label={translate('settings.close')} onClick={onClose}><CloseIcon size={18} /></button></header>
      <div className="settings-layout">
        <nav className="settings-nav" aria-label={translate('settings.groups')}>
          {catalog?.namespaces.map(group => <button type="button" key={settingsIdentity(group)} disabled={busy || dirty} aria-current={!creating && selected !== undefined && settingsIdentity(group) === settingsIdentity(selected) ? 'true' : undefined} onClick={() => select(group)}><span>{translate(settingsLabels[group.ns])}</span><strong>{group.name}</strong></button>)}
          {catalog?.writable && <button type="button" disabled={busy || dirty} onClick={createMCP}>{translate('settings.addMCP')}</button>}
          <button type="button" className="settings-reload" disabled={busy || dirty} onClick={() => void reload()}>{translate('settings.reload')}</button>
        </nav>
        <div className="settings-content" aria-busy={busy}>
          {failure !== undefined && <div className="settings-error" role="alert"><p>{translate(failure === 'conflict' ? 'settings.conflict' : failure === 'load' ? 'settings.loadFailure' : 'settings.saveFailure')}</p>{failure === 'conflict' && <button type="button" className="credential-cancel" disabled={busy} onClick={() => void reload()}>{translate('settings.reloadDiscard')}</button>}</div>}
          {catalog === undefined && failure === undefined && <p role="status">{translate('settings.loading')}</p>}
          {catalog !== undefined && !catalog.writable && <p className="settings-help">{translate('settings.readOnly')}</p>}
          {selected !== undefined && <>
            <h3>{translate(settingsLabels[selected.ns])} / {creating ? translate('settings.newGroup') : selected.name}</h3>
            {!creating && <p className="settings-help">{translate(selected.user == null ? 'settings.inherited' : 'settings.overridden')}</p>}
            {selected.pendingRestart && <p className="settings-status" role="status">{translate('settings.pendingRestart')}</p>}
            {selected.applyError && <p className="settings-error" role="status">{translate('settings.applyFailure')}</p>}
            {saved && !selected.pendingRestart && !selected.applyError && <p className="settings-status" role="status">{translate('settings.saved')}</p>}
            <form onSubmit={event => { event.preventDefault(); void write('update') }}>
              <fieldset disabled={busy || !catalog?.writable}>
                {creating && <label className="settings-field" htmlFor="settings-new-name">{translate('mcp.name')}<input id="settings-new-name" className="credential-input" required autoComplete="off" value={newName} onChange={event => { setNewName(event.target.value); setDirty(true) }} /></label>}
                <SettingsFields kind={selected.ns} value={draft} onChange={change} />
              </fieldset>
              <p className="settings-help">{translate('settings.replaceHint')}</p>
              <div className="settings-actions">
                {selected.ns !== 'mcp' && <button type="button" className="credential-cancel" disabled={busy || !catalog?.writable} onClick={() => change({})}>{translate('settings.defaults')}</button>}
                <button type="submit" className="credential-save" disabled={busy || !catalog?.writable || failure === 'conflict' || (!dirty && !creating)}>{translate(busy ? 'settings.saving' : 'settings.save')}</button>
              </div>
            </form>
            <div className="settings-actions settings-secondary-actions">
              {!creating && selected.user != null && <button type="button" className="credential-remove" disabled={busy || dirty || !catalog?.writable || failure === 'conflict'} onClick={() => void write('reset')}>{translate('settings.reset')}</button>}
              {selected.applyError && <button type="button" className="credential-cancel" disabled={busy || dirty || !catalog?.writable || failure === 'conflict'} onClick={() => void write('retry')}>{translate('settings.retry')}</button>}
              {(dirty || creating) && <button type="button" className="credential-cancel" disabled={busy} onClick={() => select(creating ? catalog?.namespaces[0] : selected)}>{translate('settings.discard')}</button>}
            </div>
            {!creating && <details className="settings-advanced"><summary>{translate('settings.sources')}</summary><h4>{translate('settings.local')}</h4><pre>{JSON.stringify(selected.base ?? null, null, 2)}</pre><h4>{translate('settings.user')}</h4><pre>{JSON.stringify(selected.user ?? null, null, 2)}</pre></details>}
            {selected.ns === 'llmprovider' && !creating && <SettingsCredential key={settingsIdentity(selected) + keyReference} api={api} reference={keyReference} onChanged={onCredentialChanged} />}
          </>}
        </div>
      </div>
    </section>
  </div>
}
