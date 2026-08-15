import type { ConversationSnapshot } from '../types'
import type { ConversationStore } from '../conversation-store'
import { ActivityIcon, DatabaseIcon, FolderIcon } from '../icons'

interface DetailsPanelProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
}

export function DetailsPanel({ store, snapshot }: DetailsPanelProps): React.JSX.Element {
  const current = store.currentSession()
  const events = store.currentEvents()
  const lastEvent = events.at(-1)
  return (
    <aside className="details-panel" aria-label="会话详情">
      <header className="flex h-[68px] shrink-0 items-center border-b border-black/[0.06] px-5">
        <div>
          <div className="font-mono text-[9px] tracking-[0.12em] text-caption">CONTEXT</div>
          <div className="mt-1 text-sm font-semibold text-ink">当前会话</div>
        </div>
      </header>

      <div className="details-scroll">
        <DetailSection title="执行状态">
          <FactRow icon={<ActivityIcon size={16} />} label="Agent" value={current?.running ? '运行中' : '空闲'} accent={current?.running === true} />
          <FactRow icon={<DatabaseIcon size={16} />} label="事实边界" value={lastEvent === undefined ? '暂无事件' : `${lastEvent.type} · ${String(lastEvent.seq)}`} />
          <FactRow icon={<FolderIcon size={16} />} label="工作区" value={current?.cwd ?? snapshot.host?.cwd ?? '未设置'} />
        </DetailSection>

        <DetailSection title="模型路由">
          <Definition label="Provider" value={snapshot.host?.provider ?? 'DeepSeek'} />
          <Definition label="Model" value={snapshot.host?.model ?? 'default'} />
          <Definition label="Session" value={current?.sessionId ?? '尚未选择'} mono />
        </DetailSection>

        <DetailSection title="主流程">
          <ol className="pipeline">
            {['Browser', 'Echo Host', 'Agent Loop', 'DeepSeek'].map((step, index) => (
              <li key={step}>
                <span>{String(index + 1).padStart(2, '0')}</span>
                <strong>{step}</strong>
              </li>
            ))}
          </ol>
        </DetailSection>

        <p className="px-1 text-[11px] leading-5 text-caption">这里只展示 Host 已提供的事实。设置、插件清单和文件系统能力没有在浏览器中伪造。</p>
      </div>
    </aside>
  )
}

function DetailSection({ title, children }: { title: string; children: React.ReactNode }): React.JSX.Element {
  return (
    <section className="border-b border-black/[0.055] py-5">
      <h2 className="mb-3 font-mono text-[9px] font-semibold tracking-[0.12em] text-caption">{title.toUpperCase()}</h2>
      <div className="space-y-3">{children}</div>
    </section>
  )
}

function FactRow({ icon, label, value, accent = false }: { icon: React.ReactNode; label: string; value: string; accent?: boolean }): React.JSX.Element {
  return (
    <div className="grid grid-cols-[24px_minmax(0,1fr)] items-start gap-2">
      <span className={accent ? 'text-brand' : 'text-caption'}>{icon}</span>
      <span className="min-w-0">
        <span className="block text-[11px] text-caption">{label}</span>
        <span className={`mt-0.5 block break-words text-xs font-medium ${accent ? 'text-brand' : 'text-secondary'}`}>{value}</span>
      </span>
    </div>
  )
}

function Definition({ label, value, mono = false }: { label: string; value: string; mono?: boolean }): React.JSX.Element {
  return (
    <div className="flex items-start justify-between gap-4 text-xs">
      <span className="text-caption">{label}</span>
      <span className={`min-w-0 break-all text-right font-medium text-secondary ${mono ? 'font-mono text-[10px]' : ''}`}>{value}</span>
    </div>
  )
}
