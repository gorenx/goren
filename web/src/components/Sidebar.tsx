import type { ConversationSnapshot, SessionSummary } from '../types'
import { GorenMark, PanelIcon, PlusIcon } from '../icons'
import type { ConversationStore } from '../conversation-store'

interface SidebarProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
  collapsed: boolean
  onToggle: () => void
}

export function Sidebar({ store, snapshot, collapsed, onToggle }: SidebarProps): React.JSX.Element {
  const connectionLabel = snapshot.onlineDownlinks === 2
    ? 'Host 已连接'
    : snapshot.onlineDownlinks === 1 ? '部分连接' : '正在重新连接'
  return (
    <aside className="sidebar" aria-label="会话导航">
      <header className="flex h-[68px] shrink-0 items-center gap-3 px-4">
        <button
          type="button"
          className="brand-button group"
          aria-label="创建新对话"
          onClick={() => void store.createSession()}
        >
          <span className="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-white text-ink shadow-[0_1px_2px_rgba(15,17,21,.08)] ring-1 ring-black/6">
            <GorenMark size={25} />
          </span>
          {!collapsed && (
            <span className="min-w-0 text-left">
              <span className="block font-display text-[15px] font-semibold tracking-[-0.02em] text-ink">Goren</span>
              <span className="block font-mono text-[9px] tracking-[0.15em] text-caption">HARNESS · GO</span>
            </span>
          )}
        </button>
        <button type="button" className="icon-button ml-auto" aria-label={collapsed ? '展开侧栏' : '收起侧栏'} onClick={onToggle}>
          <PanelIcon size={17} />
        </button>
      </header>

      <button id="new-session" type="button" className="new-session" onClick={() => void store.createSession()}>
        <PlusIcon size={17} />
        {!collapsed && <span>新对话</span>}
      </button>

      {!collapsed && <div className="px-5 pb-2 pt-3 font-mono text-[10px] font-medium tracking-[0.12em] text-caption">RECENT SESSIONS</div>}
      <nav id="session-list" className="session-list" aria-label="对话列表">
        {snapshot.sessions.map(summary => (
          <SessionButton
            key={summary.sessionId}
            summary={summary}
            selected={summary.sessionId === snapshot.currentSessionId}
            collapsed={collapsed}
            title={store.sessionTitle(summary)}
            relativeTime={store.relativeTime(summary.updatedAt)}
            onSelect={() => void store.selectSession(summary.sessionId)}
          />
        ))}
      </nav>

      <footer className="mt-auto shrink-0 border-t border-black/[0.05] p-4">
        <div className={`flex items-center ${collapsed ? 'justify-center' : 'gap-2.5'}`}>
          <span className={`connection-dot ${snapshot.onlineDownlinks > 0 ? 'is-online' : ''}`} aria-hidden="true" />
          {!collapsed && (
            <span className="min-w-0">
              <span className="block text-xs font-medium text-secondary">{connectionLabel}</span>
              <span className="block truncate font-mono text-[9px] text-caption">{snapshot.host?.version ?? 'booting'}</span>
            </span>
          )}
        </div>
      </footer>
    </aside>
  )
}

interface SessionButtonProps {
  summary: SessionSummary
  selected: boolean
  collapsed: boolean
  title: string
  relativeTime: string
  onSelect: () => void
}

function SessionButton({ summary, selected, collapsed, title, relativeTime, onSelect }: SessionButtonProps): React.JSX.Element {
  return (
    <button
      type="button"
      className={`session-item${summary.running ? ' is-running' : ''}`}
      data-session-id={summary.sessionId}
      aria-current={selected}
      aria-label={title}
      title={collapsed ? title : undefined}
      onClick={onSelect}
    >
      <span className="session-running-mark" aria-hidden="true" />
      {collapsed
        ? <span className="font-display text-xs font-semibold uppercase text-secondary">{title.slice(0, 2)}</span>
        : (
          <>
            <span className="session-title">{title}</span>
            <span className="session-meta">{summary.running ? 'RUNNING' : relativeTime}</span>
          </>
        )}
    </button>
  )
}
