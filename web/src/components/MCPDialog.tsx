import { useEffect, useRef, useState } from 'react'
import { HarnessAPI } from '../api'
import { CloseIcon } from '../icons'
import { useI18n } from '../i18n'

interface MCPView {
  serverName: string
  transport: string
  status: 'connected' | 'failed'
  toolCount: number
}

export function MCPDialog({ onClose }: { onClose: () => void }): React.JSX.Element {
  const { translate } = useI18n()
  const translateRef = useRef(translate)
  translateRef.current = translate
  const closeRef = useRef(onClose)
  closeRef.current = onClose
  const generation = useRef(0)
  const pending = useRef(false)
  const [api] = useState(() => new HarnessAPI(() => {}, () => {}, () => {}, (key, values) => translateRef.current(key, values)))
  const lifetime = useRef<AbortController | undefined>(undefined)
  const input = useRef<HTMLInputElement>(null)
  const [name, setName] = useState('')
  const [url, setURL] = useState('')
  const [views, setViews] = useState<MCPView[]>()
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    lifetime.current = controller
    input.current?.focus()
    let timer: ReturnType<typeof setTimeout>
    const refresh = async (): Promise<void> => {
      if (pending.current) {
        timer = setTimeout(() => void refresh(), 3000)
        return
      }
      const captured = generation.current
      try {
        const current = await api.call<MCPView[]>('mcp.list', {}, controller.signal)
        if (!controller.signal.aborted && captured === generation.current) setViews(current)
      } catch {
        if (!controller.signal.aborted && captured === generation.current) setFailed(true)
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(() => void refresh(), 3000)
      }
    }
    void refresh()
    const escape = (event: KeyboardEvent): void => { if (event.key === 'Escape') closeRef.current() }
    document.addEventListener('keydown', escape)
    return () => {
      controller.abort()
      clearTimeout(timer)
      document.removeEventListener('keydown', escape)
    }
  }, [api])

  const perform = async (method: 'mcp.connect' | 'mcp.disconnect', payload: object): Promise<void> => {
    const signal = lifetime.current?.signal
    if (pending.current) return
    pending.current = true
    generation.current++
    setBusy(true)
    setFailed(false)
    try {
      await api.call(method, payload, signal)
      const current = await api.call<MCPView[]>('mcp.list', {}, signal)
      if (signal?.aborted) return
      setViews(current)
      if (method === 'mcp.connect') {
        setName('')
        setURL('')
      }
    } catch {
      if (!signal?.aborted) setFailed(true)
    } finally {
      pending.current = false
      if (!signal?.aborted) setBusy(false)
    }
  }

  return (
    <div className="credential-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}>
      <section className="credential-dialog max-h-[90dvh] overflow-y-auto" role="dialog" aria-modal="true" aria-labelledby="mcp-title">
        <header className="flex items-start gap-3 border-b border-black/[0.06] px-5 py-4">
          <div className="min-w-0 flex-1">
            <h2 id="mcp-title" className="font-display text-base font-semibold text-ink">{translate('mcp.manage')}</h2>
            <p className="mt-1 text-xs leading-5 text-tertiary">{translate('mcp.description')}</p>
          </div>
          <button type="button" className="icon-button" aria-label={translate('mcp.close')} onClick={onClose}><CloseIcon size={16} /></button>
        </header>
        <form className="space-y-3 p-5" onSubmit={event => { event.preventDefault(); void perform('mcp.connect', { serverName: name.trim(), url: url.trim() }) }}>
          <p className="text-xs leading-5 text-secondary">{translate('mcp.scope')}</p>
          <label className="block text-xs text-secondary" htmlFor="mcp-name">{translate('mcp.name')}</label>
          <input ref={input} id="mcp-name" className="credential-input" required maxLength={128} autoComplete="off" placeholder="my-tools" value={name} disabled={busy} onChange={event => setName(event.target.value)} />
          <label className="block text-xs text-secondary" htmlFor="mcp-url">{translate('mcp.url')}</label>
          <input id="mcp-url" className="credential-input" type="url" required pattern="https?://.+" autoComplete="off" placeholder="http://localhost:3000/mcp" value={url} disabled={busy} onChange={event => setURL(event.target.value)} />
          {failed && <p role="alert" className="text-xs text-red-600">{translate('mcp.failure')}</p>}
          <div className="flex justify-end"><button type="submit" className="credential-save" disabled={busy || name.trim() === '' || url.trim() === ''}>{translate(busy ? 'mcp.connecting' : 'mcp.connect')}</button></div>
        </form>
        <div className="space-y-3 border-t border-black/[0.06] p-5" aria-live="polite">
          {views === undefined ? <p className="text-xs text-tertiary">{translate('mcp.loading')}</p> : views.length === 0 ? <p className="text-xs text-tertiary">{translate('mcp.empty')}</p> : views.map(view => (
            <div key={view.serverName} className="flex items-center gap-3 rounded-xl bg-black/[0.025] p-3">
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-ink">{view.serverName}</p>
                <p className="mt-1 text-xs text-tertiary">{view.status === 'connected' ? translate('mcp.connected', { count: view.toolCount }) : translate('mcp.failed')}</p>
              </div>
              <button type="button" className="credential-remove" disabled={busy} onClick={() => void perform('mcp.disconnect', { serverName: view.serverName })}>{translate('mcp.disconnect')}</button>
            </div>
          ))}
        </div>
      </section>
    </div>
  )
}
