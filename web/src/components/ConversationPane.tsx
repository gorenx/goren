import { useEffect, useRef } from 'react'
import type { ConversationSnapshot, MessageRow } from '../types'
import type { ConversationStore } from '../conversation-store'
import { ActivityIcon, GorenMark } from '../icons'
import { Composer } from './Composer'

interface ConversationPaneProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
}

export function ConversationPane({ store, snapshot }: ConversationPaneProps): React.JSX.Element {
  const current = store.currentSession()
  const rows = store.currentMessages()
  const events = store.currentEvents()
  const title = current === undefined ? '新对话' : store.sessionTitle(current)
  const lastEvent = events.at(-1)
  const durable = current?.running === false && lastEvent?.type === 'turn/end'

  return (
    <main className="conversation-column">
      <header className="conversation-header">
        <div className="min-w-0">
          <div className="flex items-center gap-2 font-mono text-[9px] tracking-[0.1em] text-caption">
            <span>SESSION</span>
            <span className="text-black/20">/</span>
            <span className="truncate">{snapshot.currentSessionId?.slice(0, 12) ?? 'NEW'}</span>
          </div>
          <h1 id="conversation-title" className="mt-1 truncate font-display text-[17px] font-semibold tracking-[-0.025em] text-ink">{title}</h1>
        </div>
        <div className="ml-auto flex items-center gap-2">
          <span className="model-chip">{snapshot.host?.provider ?? 'DeepSeek'} · {snapshot.host?.model ?? 'default'}</span>
        </div>
      </header>

      <div className={`runtime-rail ${current?.running ? 'is-running' : ''}`} aria-label="当前运行状态">
        <RailItem label="HOST" value={snapshot.onlineDownlinks === 2 ? 'LIVE' : snapshot.onlineDownlinks === 1 ? 'PARTIAL' : 'CONNECTING'} active={snapshot.onlineDownlinks > 0} />
        <RailItem label="AGENT" value={current?.running ? 'RUNNING' : 'IDLE'} active={current?.running === true} />
        <RailItem label="FACTS" value={durable ? `DURABLE · ${String(lastEvent.seq)}` : `${String(events.length)} EVENTS`} active={durable} />
      </div>

      <MessageList rows={rows} cwd={snapshot.host?.cwd} />
      <Composer store={store} snapshot={snapshot} />
    </main>
  )
}

function RailItem({ label, value, active }: { label: string; value: string; active: boolean }): React.JSX.Element {
  return (
    <span className="runtime-item">
      <span className={`runtime-dot ${active ? 'is-active' : ''}`} aria-hidden="true" />
      <span>{label}</span>
      <strong>{value}</strong>
    </span>
  )
}

function MessageList({ rows, cwd }: { rows: readonly MessageRow[]; cwd?: string }): React.JSX.Element {
  const scrollport = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const element = scrollport.current
    if (element !== null) element.scrollTop = element.scrollHeight
  }, [rows])

  return (
    <section ref={scrollport} id="messages" className="messages" aria-live="polite" aria-label="对话内容">
      {rows.length === 0
        ? <EmptyConversation cwd={cwd} />
        : (
          <div className="mx-auto flex w-full max-w-[780px] flex-col gap-7 px-5 pb-10 pt-8 sm:px-8">
            {rows.map((row, index) => <Message key={`${row.role}-${String(row.seq ?? index)}`} row={row} />)}
          </div>
        )}
    </section>
  )
}

function EmptyConversation({ cwd }: { cwd?: string }): React.JSX.Element {
  const workspaceName = cwd?.split(/[\\/]/).filter(Boolean).at(-1)
  return (
    <div className="empty-state">
      <div className="hero-halo" aria-hidden="true" />
      <div className="relative z-10 flex flex-col items-center">
        <div className="hero-mark"><GorenMark size={41} /></div>
        <div className="mt-5 flex items-center gap-2">
          <h2 className="font-display text-[27px] font-medium tracking-[-0.04em] text-ink">开始一段可恢复的对话</h2>
          <span className="rounded-full bg-brand-soft px-2 py-0.5 font-mono text-[9px] font-semibold tracking-[0.08em] text-brand">GO</span>
        </div>
        <p className="mt-2 max-w-md text-center text-sm leading-6 text-tertiary">每个回复都经过 Go Agent Loop，并以 Session facts 保留上下文。</p>
        {workspaceName !== undefined && (
          <div className="mt-5 inline-flex max-w-md items-center gap-2 rounded-xl border border-black/[0.07] bg-white/80 px-3 py-2 text-xs text-secondary shadow-[0_1px_2px_rgba(15,17,21,.04)] backdrop-blur">
            <ActivityIcon size={15} className="text-brand" />
            <span>当前工作区</span>
            <span className="truncate font-medium text-ink">{workspaceName}</span>
          </div>
        )}
      </div>
    </div>
  )
}

function Message({ row }: { row: MessageRow }): React.JSX.Element {
  return (
    <article className={`message ${row.role}${row.streaming ? ' streaming' : ''}`}>
      <div className="message-role">
        {row.role === 'assistant'
          ? <span className="assistant-mark"><GorenMark size={18} /></span>
          : <span className="user-mark">你</span>}
        <span>{row.role === 'assistant' ? 'Goren Agent' : 'You'}</span>
        {row.streaming && <span className="stream-label">STREAMING</span>}
      </div>
      <div className="message-body">
        {row.reasoning !== '' && (
          <details className="reasoning" open={row.streaming}>
            <summary>思考过程</summary>
            <p>{row.reasoning}</p>
          </details>
        )}
        {row.text !== '' && <p>{row.text}</p>}
        {row.streaming && <span className="stream-caret" aria-hidden="true" />}
      </div>
    </article>
  )
}
