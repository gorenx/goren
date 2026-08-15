import type { ConversationSnapshot } from '../types'
import type { ConversationStore } from '../conversation-store'
import { ActivityIcon, DatabaseIcon, FolderIcon, KeyIcon } from '../icons'
import { useI18n } from '../i18n'

interface DetailsPanelProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
}

export function DetailsPanel({ store, snapshot }: DetailsPanelProps): React.JSX.Element {
  const { translate } = useI18n()
  const current = store.currentSession()
  const events = store.currentEvents()
  const lastEvent = events.at(-1)
  return (
    <aside className="details-panel" aria-label={translate('details.label')}>
      <header className="flex h-[68px] shrink-0 items-center border-b border-black/[0.06] px-5">
        <div>
          <div className="font-mono text-[9px] uppercase tracking-[0.12em] text-caption">{translate('details.context')}</div>
          <div className="mt-1 text-sm font-semibold text-ink">{translate('details.currentSession')}</div>
        </div>
      </header>

      <div className="details-scroll">
        <DetailSection title={translate('details.executionStatus')}>
          <FactRow icon={<ActivityIcon size={16} />} label={translate('details.agent')} value={current?.running ? translate('conversation.running') : translate('conversation.idle')} accent={current?.running === true} />
          <FactRow icon={<DatabaseIcon size={16} />} label={translate('details.factBoundary')} value={lastEvent === undefined ? translate('details.noEvents') : `${lastEvent.type} · ${String(lastEvent.seq)}`} />
          <FactRow icon={<FolderIcon size={16} />} label={translate('details.workspace')} value={current?.cwd ?? snapshot.host?.cwd ?? translate('details.notSet')} />
        </DetailSection>

        <DetailSection title={translate('details.modelRoute')}>
          <Definition label="Provider" value={snapshot.host?.provider ?? 'DeepSeek'} />
          <Definition label="Model" value={snapshot.host?.model ?? 'default'} />
          <Definition label="API Key" value={snapshot.credential?.configured ? translate('details.configured', { source: snapshot.credential.source ?? 'unknown' }) : translate('details.notConfigured')} />
          <Definition label="Session" value={current?.sessionId ?? translate('details.noSession')} mono />
        </DetailSection>

        <DetailSection title={translate('details.mainFlow')}>
          <ol className="pipeline">
            {[translate('details.browser'), 'Echo Host', 'Agent Loop', 'DeepSeek'].map((step, index) => (
              <li key={step}>
                <span>{String(index + 1).padStart(2, '0')}</span>
                <strong>{step}</strong>
              </li>
            ))}
          </ol>
        </DetailSection>

        <p className="flex gap-2 px-1 text-[11px] leading-5 text-caption"><KeyIcon size={14} className="mt-0.5 shrink-0" />{translate('details.note')}</p>
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
