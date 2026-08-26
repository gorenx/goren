import type { ConversationSnapshot, SessionSummary } from '../types'
import { GorenMark, PanelIcon, PlusIcon } from '../icons'
import type { ConversationStore } from '../conversation-store'
import { useI18n } from '../i18n'

interface SidebarProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
  collapsed: boolean
  onToggle: () => void
}

export function Sidebar({ store, snapshot, collapsed, onToggle }: SidebarProps): React.JSX.Element {
  const { activeLanguage, translate } = useI18n()
  const createLabel = snapshot.creatingSession
    ? translate('sidebar.creatingConversation')
    : translate('sidebar.newConversation')
  const connectionLabel = snapshot.onlineDownlinks === 2
    ? translate('sidebar.hostConnected')
    : snapshot.onlineDownlinks === 1 ? translate('sidebar.partiallyConnected') : translate('sidebar.reconnecting')
  return (
    <aside className="sidebar" aria-label={translate('sidebar.navigation')}>
      <header className="flex h-[68px] shrink-0 items-center gap-3 px-4">
        <button
          type="button"
          className="brand-button group"
          disabled={snapshot.creatingSession}
          aria-busy={snapshot.creatingSession}
          aria-label={createLabel}
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
        <button type="button" className="icon-button ml-auto" aria-label={collapsed ? translate('sidebar.expand') : translate('sidebar.collapse')} onClick={onToggle}>
          <PanelIcon size={17} />
        </button>
      </header>

      <button
        id="new-session"
        type="button"
        className="new-session"
        disabled={snapshot.creatingSession}
        aria-busy={snapshot.creatingSession}
        aria-label={createLabel}
        onClick={() => void store.createSession()}
      >
        <PlusIcon size={17} />
        {!collapsed && <span>{createLabel}</span>}
      </button>

      {!collapsed && <div className="px-5 pb-2 pt-3 font-mono text-[10px] font-medium uppercase tracking-[0.12em] text-caption">{translate('sidebar.recentSessions')}</div>}
      <nav id="session-list" className="session-list" aria-label={translate('sidebar.conversationList')}>
        {snapshot.sessions.map(summary => (
          <SessionButton
            key={summary.sessionId}
            summary={summary}
            selected={summary.sessionId === snapshot.currentSessionId}
            collapsed={collapsed}
            title={store.sessionTitle(summary)}
            relativeTime={store.relativeTime(summary.updatedAt, activeLanguage)}
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
              <span className="block truncate font-mono text-[9px] text-caption">{snapshot.host?.version ?? translate('sidebar.booting')}</span>
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
  const { translate } = useI18n()
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
            <span className="session-meta">{summary.running ? translate('sidebar.running') : relativeTime}</span>
          </>
        )}
    </button>
  )
}
