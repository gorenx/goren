import { useState } from 'react'
import type { ConversationSnapshot, SessionSummary } from '../types'
import { GorenMark, PanelIcon, PlusIcon } from '../icons'
import type { ConversationStore } from '../conversation-store'
import { useI18n } from '../i18n'

interface SidebarProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
  collapsed: boolean
  onToggle: () => void
  onOpenCompanions: () => void
  onOpenMCP: () => void
  onOpenSettings: () => void
}

export function Sidebar({ store, snapshot, collapsed, onToggle, onOpenCompanions, onOpenMCP, onOpenSettings }: SidebarProps): React.JSX.Element {
  const { activeLanguage, translate } = useI18n()
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set())
  const rows = sessionBranches(snapshot.sessions, expanded)
  const toggleChildren = (identifier: string): void => {
    setExpanded(previous => {
      const next = new Set(previous)
      if (!next.delete(identifier)) next.add(identifier)
      return next
    })
  }
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
      <nav
        id="session-list"
        className="session-list"
        aria-label={translate('sidebar.conversationList')}
        onScroll={event => {
          const list = event.currentTarget
          if (list.scrollHeight - list.scrollTop - list.clientHeight < 80) {
            void store.loadMoreSessions()
          }
        }}
      >
        {rows.map(({ summary, depth, children }) => (
          <div key={summary.sessionId} className={`session-branch${depth > 0 ? ' is-child' : ''}`} style={{ paddingInlineStart: `${Math.min(depth, 8) * (collapsed ? 8 : 16)}px` }}>
            {depth > 0 && <span className="session-branch-line" style={{ insetInlineStart: `${Math.max(0, Math.min(depth, 8) * (collapsed ? 8 : 16) - 9)}px` }} aria-hidden="true" />}
            <SessionButton
              summary={summary}
              selected={summary.sessionId === snapshot.currentSessionId}
              collapsed={collapsed}
              title={store.sessionTitle(summary)}
              relativeTime={store.relativeTime(summary.updatedAt, activeLanguage)}
              onSelect={() => void store.selectSession(summary.sessionId)}
            />
            {children && <button type="button" className="session-branch-toggle" aria-expanded={expanded.has(summary.sessionId)} aria-label={translate(expanded.has(summary.sessionId) ? 'sidebar.collapseChildren' : 'sidebar.expandChildren', { name: store.sessionTitle(summary) })} onClick={() => toggleChildren(summary.sessionId)}>{expanded.has(summary.sessionId) ? '⌄' : '›'}</button>}
          </div>
        ))}
        {!collapsed && snapshot.nextSessionCursor !== undefined && (
          <button
            type="button"
            className="mx-3 my-2 rounded-lg px-3 py-2 text-xs text-secondary hover:bg-black/[0.04] disabled:opacity-60"
            disabled={snapshot.loadingMoreSessions}
            onClick={() => void store.loadMoreSessions()}
          >
            {translate(snapshot.loadingMoreSessions ? 'sidebar.loadingSessions' : 'sidebar.loadMoreSessions')}
          </button>
        )}
      </nav>

    <footer className="mt-auto shrink-0 border-t border-black/[0.05] p-4">
      <button type="button" className="mb-3 w-full rounded-lg border border-black/[0.08] bg-white px-2 py-2 text-xs font-medium text-secondary hover:text-ink" aria-label={translate('settings.title')} title={translate('settings.title')} onClick={onOpenSettings}>{collapsed ? '⚙' : translate('settings.title')}</button>
      <button type="button" className="mb-3 w-full rounded-lg border border-black/[0.08] bg-white px-2 py-2 text-xs font-medium text-secondary hover:text-ink" aria-label={translate('companion.manage')} title={translate('companion.manage')} onClick={onOpenCompanions}>{collapsed ? 'C' : translate('companion.manage')}</button>
      <button
        type="button"
        className="mb-3 w-full rounded-lg border border-black/[0.08] bg-white px-2 py-2 text-xs font-medium text-secondary hover:text-ink"
        aria-label={translate('mcp.manage')}
        title={translate('mcp.manage')}
        onClick={onOpenMCP}
      >
        {collapsed ? 'M' : translate('mcp.manage')}
      </button>

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
      <span className={`session-status-dot${summary.running ? ' is-running' : ''}`} role="img" aria-label={translate(summary.running ? 'sidebar.running' : 'sidebar.notRunning')} title={translate(summary.running ? 'sidebar.running' : 'sidebar.notRunning')} />
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

// Parent links come from Session summaries. Missing parents remain navigable while
// older pages load; the visited set also bounds malformed cyclic relationships.
function sessionBranches(summaries: readonly SessionSummary[], expanded: ReadonlySet<string>): Array<{ summary: SessionSummary; depth: number; children: boolean }> {
  const known = new Set(summaries.map(entry => entry.sessionId))
  const children = new Map<string, SessionSummary[]>()
  for (const entry of summaries) {
    if (entry.parentSessionId !== undefined && entry.parentSessionId !== entry.sessionId && known.has(entry.parentSessionId)) {
      children.set(entry.parentSessionId, [...(children.get(entry.parentSessionId) ?? []), entry])
    }
  }
  const rows: Array<{ summary: SessionSummary; depth: number; children: boolean }> = []
  const visited = new Set<string>()
  const visit = (entry: SessionSummary, depth: number, visible: boolean): void => {
    if (visited.has(entry.sessionId)) return
    visited.add(entry.sessionId)
    const descendants = children.get(entry.sessionId) ?? []
    if (visible) rows.push({ summary: entry, depth, children: descendants.length > 0 })
    for (const child of descendants) visit(child, depth + 1, visible && expanded.has(entry.sessionId))
  }
  for (const entry of summaries) {
    if (entry.parentSessionId === undefined || !known.has(entry.parentSessionId) || entry.parentSessionId === entry.sessionId) visit(entry, entry.parentSessionId === undefined ? 0 : 1, true)
  }
  for (const entry of summaries) if (!visited.has(entry.sessionId)) visit(entry, 0, true)
  return rows
}
