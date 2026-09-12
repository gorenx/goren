import { useEffect, useRef, useState } from 'react'
import { HarnessAPI, RPCError } from '../api'
import { companionPhases, companionTransitioning, decodeCompanions, type Companion, type CompanionListing } from '../companion'
import { CloseIcon } from '../icons'
import { useI18n } from '../i18n'
import { decodeSettingsCatalog, type SettingsCatalog, type SettingsKind } from '../settings'
import { SettingsDialog } from './SettingsDialog'

export function CompanionDialog({ sessionId, onClose }: { sessionId?: string; onClose: () => void }): React.JSX.Element {
  const { translate } = useI18n()
  const translateRef = useRef(translate)
  translateRef.current = translate
  const [api] = useState(() => new HarnessAPI(() => {}, () => {}, () => {}, (key, values) => translateRef.current(key, values)))
  const [catalog, setCatalog] = useState<SettingsCatalog>()
  const [listing, setListing] = useState<CompanionListing>()
  const [selection, setSelection] = useState('')
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<'load' | 'action' | 'conflict'>()
  const [editor, setEditor] = useState<{ ns: SettingsKind; name?: string }>()
  const dialog = useRef<HTMLElement>(null)
  const lifetime = useRef<AbortController | undefined>(undefined)
  const generation = useRef(0)
  const pending = useRef(false)
  const reload = useRef<(() => Promise<void>) | undefined>(undefined)

  useEffect(() => {
    if (editor !== undefined) return
    const controller = new AbortController()
    lifetime.current = controller
    pending.current = false
    generation.current++
    setBusy(false)
    setListing(undefined)
    setCatalog(undefined)
    setFailure(undefined)
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
    dialog.current?.focus()
    let timer: ReturnType<typeof setTimeout> | undefined
    const refresh = async (): Promise<void> => {
      if (pending.current || controller.signal.aborted) return
      const captured = ++generation.current
      try {
        const [settings, companions] = await Promise.all([
          api.call('settings.describe', {}, controller.signal).then(decodeSettingsCatalog),
          sessionId === undefined ? Promise.resolve(undefined) : api.call('companion.list', { parentSessionId: sessionId }, controller.signal).then(raw => decodeCompanions(raw, sessionId)),
        ])
        if (controller.signal.aborted || captured !== generation.current) return
        setCatalog(settings)
        setListing(companions)
        setFailure(current => current === 'load' ? undefined : current)
      } catch {
        if (!controller.signal.aborted && captured === generation.current) setFailure('load')
      }
    }
    reload.current = refresh
    const poll = async (): Promise<void> => {
      await refresh()
      if (!controller.signal.aborted) timer = setTimeout(() => void poll(), 2000)
    }
    void poll()
    return () => {
      controller.abort()
      clearTimeout(timer)
      previousFocus?.focus()
    }
  }, [api, sessionId, editor])

  const act = async (method: 'create' | 'configure' | 'remove' | 'restart', entry?: Companion): Promise<void> => {
    if (pending.current || sessionId === undefined || lifetime.current === undefined) return
    if (method === 'remove' && !globalThis.confirm(translate('companion.confirmRemove', { name: entry?.name ?? '' }))) return
    const signal = lifetime.current.signal
    pending.current = true
    generation.current++
    setBusy(true)
    setFailure(undefined)
    const payload = method === 'create'
      ? { parentSessionId: sessionId, name: selection }
      : { parentSessionId: sessionId, name: entry!.name, expectedRevision: entry!.revision,
        ...(method === 'restart' ? { expectedCreationId: entry!.creationId ?? '' } : {}),
        ...(method === 'configure' ? { enabled: !entry!.enabled } : {}) }
    try {
      await api.call(`companion.${method}`, payload, signal)
      if (!signal.aborted && method === 'create') setSelection('')
    } catch (error) {
      if (!signal.aborted) setFailure(error instanceof RPCError && error.code === 'companion-conflict' ? 'conflict' : 'action')
    } finally {
      if (!signal.aborted) {
        pending.current = false
        setBusy(false)
        await reload.current?.()
      }
    }
  }

  if (editor !== undefined) return <SettingsDialog initial={editor} onClose={() => setEditor(undefined)} />
  const available = catalog?.namespaces.filter(group => group.ns === 'companion' && group.value !== null && !listing?.items.some(entry => entry.name === group.name && !entry.removed)) ?? []
  const entries = listing?.items.filter(entry => !entry.removed) ?? []
  const admitted = listing?.phase === 'ready' && !listing.failure && failure !== 'load'
  const trapFocus = (event: React.KeyboardEvent): void => {
    if (event.key === 'Escape' && !busy) onClose()
    if (event.key !== 'Tab') return
    const focusable = [...(dialog.current?.querySelectorAll<HTMLElement>('button:not(:disabled), select:not(:disabled), input:not(:disabled)') ?? [])].filter(element => element.getClientRects().length > 0)
    const first = focusable[0]
    const last = focusable.at(-1)
    if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) { event.preventDefault(); last?.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
  }
  return <div className="credential-backdrop" role="presentation" onMouseDown={event => { if (!busy && event.target === event.currentTarget) onClose() }}>
    <section ref={dialog} tabIndex={-1} onKeyDown={trapFocus} className="settings-dialog" role="dialog" aria-modal="true" aria-labelledby="companion-title">
      <header className="settings-header"><div><h2 id="companion-title">{translate('companion.manage')}</h2><p className="settings-help">{translate('companion.description')}</p></div><button type="button" className="icon-button" disabled={busy} aria-label={translate('companion.close')} onClick={onClose}><CloseIcon size={18} /></button></header>
      <div className="space-y-5 overflow-y-auto p-5" aria-busy={busy}>
        <div className="flex flex-wrap items-center justify-between gap-3"><p className="text-xs text-secondary break-all">{translate(sessionId === undefined ? 'companion.noSession' : 'companion.session', { name: sessionId ?? '' })}</p><button type="button" className="credential-cancel" disabled={busy} onClick={() => setEditor({ ns: 'companion' })}>{translate('companion.settings')}</button></div>
        {failure !== undefined && <p className="settings-error" role="alert">{translate(failure === 'conflict' ? 'settings.conflict' : failure === 'load' ? 'companion.unavailable' : 'companion.failed')}</p>}
        {listing?.failure && <p className="settings-error" role="alert">{translate('companion.failed')}</p>}
        {listing !== undefined && listing.phase !== 'ready' && <p role="status" className="settings-help">{translate(companionPhases[listing.phase])}</p>}
        {sessionId !== undefined && listing === undefined && failure === undefined && <p role="status">{translate('companion.loading')}</p>}
        {admitted && entries.length === 0 && <p className="settings-help">{translate('companion.empty')}</p>}
        <div className="space-y-3" aria-live="polite">{entries.map(entry => <article key={entry.name} className="rounded-xl border border-black/[0.08] p-4">
          <div className="flex items-start justify-between gap-3"><div><h3 className="font-display text-sm font-semibold">{entry.name}</h3><p className="mt-1 text-xs text-tertiary break-all">{entry.childSessionId}</p></div><span className={`text-xs ${entry.failure ? 'text-red-600' : 'text-secondary'}`}>{translate(entry.phase === 'ready' ? entry.activity === 'running' ? 'companion.running' : entry.activity === 'idle' ? 'companion.idle' : 'companion.childUnavailable' : companionPhases[entry.phase])}</span></div>
          {entry.failure && <p className="mt-2 text-xs text-red-600">{translate('companion.failed')}</p>}
          <div className="mt-4 flex flex-wrap gap-2">
            {entry.configuration !== undefined && <button type="button" className="credential-cancel" disabled={busy} onClick={() => setEditor({ ns: 'agent', name: entry.configuration })}>{translate('companion.edit')}</button>}
            <button type="button" className="credential-cancel" disabled={busy || !admitted || !entry.enabled || companionTransitioning(entry.phase)} onClick={() => void act('restart', entry)}>{translate('companion.restart')}</button>
            <button type="button" className="credential-cancel" disabled={busy || !admitted || !entry.persisted || companionTransitioning(entry.phase)} onClick={() => void act('configure', entry)}>{translate(entry.enabled ? 'companion.disable' : 'companion.enable')}</button>
            <button type="button" className="credential-remove" disabled={busy || !admitted || !entry.persisted || companionTransitioning(entry.phase)} onClick={() => void act('remove', entry)}>{translate('companion.remove')}</button>
          </div>
        </article>)}</div>
        <form className="flex flex-wrap items-end gap-3 border-t border-black/[0.06] pt-4" onSubmit={event => { event.preventDefault(); void act('create') }}>
          <label className="settings-field min-w-48 flex-1">{translate('companion.policy')}<select className="credential-input" value={selection} disabled={busy || !admitted} onChange={event => setSelection(event.target.value)}><option value="">{translate('companion.selectPolicy')}</option>{available.map(group => <option key={group.name} value={group.name}>{group.name}</option>)}</select></label>
          <button type="submit" className="credential-save" disabled={busy || !admitted || !available.some(group => group.name === selection)}>{translate('companion.create')}</button>
          <button type="button" className="credential-cancel" disabled={busy} onClick={() => void reload.current?.()}>{translate('companion.reload')}</button>
        </form>
      </div>
    </section>
  </div>
}
