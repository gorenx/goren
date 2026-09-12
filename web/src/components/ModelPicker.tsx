import { useEffect, useState } from 'react'
import type { ConversationStore, SessionModels } from '../conversation-store'
import { useI18n } from '../i18n'
import type { SessionSummary } from '../types'

export function ModelPicker({ store, session }: { store: ConversationStore; session: SessionSummary }): React.JSX.Element {
  const sessionId = session.sessionId
  const { translate } = useI18n()
  const [catalog, setCatalog] = useState<SessionModels>()
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string>()
  useEffect(() => {
    let active = true
    setCatalog(undefined)
    setFailure(undefined)
    void store.sessionModels(sessionId).then(value => { if (active) setCatalog(value) }).catch(error => { if (active) setFailure(String(error)) })
    return () => { active = false }
  }, [store, session])
  const choices = catalog?.groups.flatMap(group => group.models.map(model => ({ provider: group.id, ...model }))) ?? []
  const selected = choices.findIndex(item => item.provider === catalog?.current?.provider && item.id === catalog?.current?.model)
  return <div className="min-w-0">
    <select className="max-w-48 bg-transparent text-xs" aria-label={translate('composer.model')} disabled={busy || catalog?.current == null} value={selected} onChange={event => {
      const candidate = choices[Number(event.currentTarget.value)]
      if (candidate === undefined) return
      setBusy(true)
      setFailure(undefined)
      void store.selectModel(sessionId, candidate.provider, candidate.id).then(() => setCatalog(current => current === undefined ? current : { ...current, current: { provider: candidate.provider, model: candidate.id } }))
        .catch(error => setFailure(String(error))).finally(() => setBusy(false))
    }}>
      {selected === -1 && <option value={-1}>{catalog?.current?.model ?? translate(catalog === undefined ? 'composer.loadingModel' : 'composer.agentInactive')}</option>}
      {choices.map((item, index) => <option key={`${item.provider}/${item.id}`} value={index}>{item.name}</option>)}
    </select>
    {failure !== undefined && <p role="alert" className="max-w-sm text-xs text-red-700">{failure}</p>}
  </div>
}
