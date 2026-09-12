import { useEffect, useRef, useState } from 'react'
import { HarnessAPI } from '../api'
import { CloseIcon } from '../icons'
import { useI18n } from '../i18n'

interface MCPView {
  serverName: string
  transport: string
  status: 'connected' | 'failed' | 'reconnecting'
  toolCount: number
}

export function MCPDialog({ sessionId, onClose, onOpenSettings }: { sessionId?: string; onClose: () => void; onOpenSettings: () => void }): React.JSX.Element {
  const { translate } = useI18n()
  const translateRef = useRef(translate)
  translateRef.current = translate
  const closeRef = useRef(onClose)
  closeRef.current = onClose
  const generation = useRef(0)
  const pending = useRef(false)
  const [api] = useState(() => new HarnessAPI(() => {}, () => {}, () => {}, (key, values) => translateRef.current(key, values)))
  const lifetime = useRef<AbortController | undefined>(undefined)
  const [views, setViews] = useState<MCPView[]>()
  const [retrying, setRetrying] = useState<string>()
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    lifetime.current = controller
    generation.current++
    pending.current = false
    setViews(undefined)
    setRetrying(undefined)
    setFailed(false)
    let timer: ReturnType<typeof setTimeout>
    const refresh = async (): Promise<void> => {
      if (sessionId === undefined) return
      const captured = generation.current
      try {
        const current = await api.call<MCPView[]>('mcp.list', { sessionId }, controller.signal)
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
  }, [api, sessionId])

  const retry = async (serverName: string): Promise<void> => {
    const signal = lifetime.current?.signal
    if (pending.current || sessionId === undefined) return
    pending.current = true
    const captured = ++generation.current
    setRetrying(serverName)
    setFailed(false)
    try {
      await api.call('mcp.retry', { sessionId, serverName }, signal)
      const current = await api.call<MCPView[]>('mcp.list', { sessionId }, signal)
      if (!signal?.aborted && captured === generation.current) setViews(current)
    } catch {
      if (!signal?.aborted && captured === generation.current) setFailed(true)
    } finally {
      if (captured === generation.current) {
        pending.current = false
        if (!signal?.aborted) setRetrying(undefined)
      }
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
        <div className="space-y-3 p-5">
          <p className="text-xs leading-5 text-secondary">{sessionId === undefined ? translate('mcp.selectSession') : translate('mcp.scope', { sessionId })}</p>
          {failed && <p role="alert" className="text-xs text-red-600">{translate('mcp.failure')}</p>}
          <button type="button" className="credential-save" onClick={onOpenSettings}>{translate('mcp.settings')}</button>
        </div>
        <div className="space-y-3 border-t border-black/[0.06] p-5" aria-live="polite">
          {sessionId === undefined ? null : views === undefined ? <p className="text-xs text-tertiary">{translate('mcp.loading')}</p> : views.length === 0 ? <p className="text-xs text-tertiary">{translate('mcp.empty')}</p> : views.map(view => (
            <div key={view.serverName} className="flex items-center gap-3 rounded-xl bg-black/[0.025] p-3">
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-ink">{view.serverName}</p>
                <p className="mt-1 text-xs text-tertiary">{retrying === view.serverName || view.status === 'reconnecting' ? translate('mcp.reconnecting') : view.status === 'connected' ? translate('mcp.connected', { count: view.toolCount }) : translate('mcp.failed')}</p>
              </div>
              <button type="button" className="credential-remove" disabled={retrying !== undefined || view.status === 'reconnecting'} onClick={() => void retry(view.serverName)}>{translate('mcp.retry')}</button>
            </div>
          ))}
        </div>
      </section>
    </div>
  )
}
