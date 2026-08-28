import { useMemo, useState } from 'react'
import type { ConversationStore } from '../conversation-store'
import { CloseIcon } from '../icons'
import { useI18n } from '../i18n'
import type { BoundDefinition, BoundDefinitionDraft, ConversationSnapshot } from '../types'

interface BoundDefinitionDialogProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
  onClose: () => void
}

type ToolMode = 'inherit' | 'allow' | 'deny' | 'both'

interface FormState {
  name: string
  revision?: number
  enabled: boolean
  systemPrompt: string
  provider: string
  model: string
  maxTokens: string
  maxDepth: string
  toolMode: ToolMode
  allow: string
  deny: string
  extensions: string
}

const emptyForm: FormState = {
  name: '',
  enabled: true,
  systemPrompt: '',
  provider: '',
  model: '',
  maxTokens: '',
  maxDepth: '',
  toolMode: 'inherit',
  allow: '',
  deny: '',
  extensions: '',
}

export function BoundDefinitionDialog({ store, snapshot, onClose }: BoundDefinitionDialogProps): React.JSX.Element {
  const { translate } = useI18n()
  const [form, setForm] = useState<FormState>(emptyForm)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string>()
  const editing = form.revision !== undefined
  const selected = useMemo(
    () => snapshot.boundDefinitions.find(definition => definition.name === form.name),
    [snapshot.boundDefinitions, form.name],
  )

  const choose = (definition: BoundDefinition): void => {
    setForm(formFromDefinition(definition))
    setFailure(undefined)
  }

  const submit = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    let draft: BoundDefinitionDraft
    try {
      draft = draftFromForm(form)
    } catch (error) {
      setFailure(error instanceof Error ? error.message : String(error))
      return
    }
    setBusy(true)
    setFailure(undefined)
    try {
      const committed = form.revision === undefined
        ? await store.createBoundDefinition(draft)
        : await store.replaceBoundDefinition(form.revision, draft)
      setForm(formFromDefinition(committed))
    } catch (error) {
      setFailure(error instanceof Error ? error.message : String(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="credential-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}>
      <section className="definition-dialog" role="dialog" aria-modal="true" aria-labelledby="bound-definition-title">
        <header className="flex items-start gap-3 border-b border-black/[0.06] px-5 py-4">
          <div className="min-w-0 flex-1">
            <h2 id="bound-definition-title" className="font-display text-base font-semibold text-ink">{translate('bound.title')}</h2>
            <p className="mt-1 text-xs leading-5 text-tertiary">{translate('bound.description')}</p>
          </div>
          <button type="button" className="icon-button" aria-label={translate('bound.close')} onClick={onClose}><CloseIcon size={16} /></button>
        </header>

        <div className="definition-layout">
          <aside className="definition-list" aria-label={translate('bound.list')}>
            <button type="button" className="definition-new" onClick={() => { setForm(emptyForm); setFailure(undefined) }}>{translate('bound.new')}</button>
            {snapshot.boundDefinitions.map(definition => (
              <button
                type="button"
                key={definition.name}
                className="definition-item"
                aria-current={selected?.name === definition.name}
                onClick={() => choose(definition)}
              >
                <span>{definition.name}</span>
                <small>{definition.enabled ? translate('bound.enabled') : translate('bound.disabled')} · r{definition.revision}</small>
              </button>
            ))}
            {snapshot.boundDefinitionsLoaded && snapshot.boundDefinitions.length === 0 && <p className="px-2 py-3 text-xs text-tertiary">{translate('bound.empty')}</p>}
          </aside>

          <form className="definition-form" onSubmit={event => void submit(event)}>
            <div className="definition-row">
              <label>{translate('bound.name')}<input name="name" className="credential-input" value={form.name} disabled={busy || editing} onChange={event => setForm({ ...form, name: event.target.value })} /></label>
              <label className="definition-check"><input type="checkbox" checked={form.enabled} disabled={busy} onChange={event => setForm({ ...form, enabled: event.target.checked })} />{translate('bound.enabled')}</label>
            </div>
            <label>{translate('bound.systemPrompt')}<textarea name="systemPrompt" className="credential-input definition-prompt" value={form.systemPrompt} disabled={busy} onChange={event => setForm({ ...form, systemPrompt: event.target.value })} /></label>
            <div className="definition-row definition-row-three">
              <label>{translate('bound.provider')}<input name="provider" className="credential-input" value={form.provider} disabled={busy} onChange={event => setForm({ ...form, provider: event.target.value })} /></label>
              <label>{translate('bound.model')}<input name="model" className="credential-input" value={form.model} disabled={busy} onChange={event => setForm({ ...form, model: event.target.value })} /></label>
              <label>{translate('bound.maxTokens')}<input name="maxTokens" className="credential-input" inputMode="numeric" value={form.maxTokens} disabled={busy} onChange={event => setForm({ ...form, maxTokens: event.target.value })} /></label>
            </div>
            <div className="definition-row">
              <label>{translate('bound.maxDepth')}<input name="maxDepth" className="credential-input" inputMode="numeric" value={form.maxDepth} disabled={busy} onChange={event => setForm({ ...form, maxDepth: event.target.value })} /></label>
              <label>{translate('bound.toolMode')}<select name="toolMode" className="credential-input" value={form.toolMode} disabled={busy} onChange={event => setForm({ ...form, toolMode: event.target.value as ToolMode })}><option value="inherit">{translate('bound.toolsInherit')}</option><option value="allow">{translate('bound.toolsAllow')}</option><option value="deny">{translate('bound.toolsDeny')}</option><option value="both">{translate('bound.toolsBoth')}</option></select></label>
            </div>
            {(form.toolMode === 'allow' || form.toolMode === 'both') && <label>{translate('bound.allowTools')}<input name="allow" className="credential-input" value={form.allow} disabled={busy} onChange={event => setForm({ ...form, allow: event.target.value })} /></label>}
            {(form.toolMode === 'deny' || form.toolMode === 'both') && <label>{translate('bound.denyTools')}<input name="deny" className="credential-input" value={form.deny} disabled={busy} onChange={event => setForm({ ...form, deny: event.target.value })} /></label>}
            <label>{translate('bound.extensions')}<input name="extensions" className="credential-input" value={form.extensions} disabled={busy} onChange={event => setForm({ ...form, extensions: event.target.value })} /></label>
            {failure !== undefined && <p className="text-xs text-red-600">{failure}</p>}
            <div className="flex justify-end gap-2">
              <button type="button" className="credential-cancel" disabled={busy} onClick={onClose}>{translate('bound.close')}</button>
              <button type="submit" className="credential-save" disabled={busy || form.name.trim() === '' || form.systemPrompt.trim() === ''}>{busy ? translate('bound.saving') : editing ? translate('bound.replace') : translate('bound.create')}</button>
            </div>
          </form>
        </div>
      </section>
    </div>
  )
}

function formFromDefinition(definition: BoundDefinition): FormState {
  const hasAllow = definition.toolRestriction?.allow !== undefined
  const hasDeny = definition.toolRestriction?.deny !== undefined
  return {
    name: definition.name,
    revision: definition.revision,
    enabled: definition.enabled,
    systemPrompt: definition.systemPrompt,
    provider: definition.agentOptions?.provider ?? '',
    model: definition.agentOptions?.model ?? '',
    maxTokens: definition.agentOptions?.maxTokens?.toString() ?? '',
    maxDepth: definition.maxDepth?.toString() ?? '',
    toolMode: hasAllow && hasDeny ? 'both' : hasAllow ? 'allow' : hasDeny ? 'deny' : 'inherit',
    allow: definition.toolRestriction?.allow?.join(', ') ?? '',
    deny: definition.toolRestriction?.deny?.join(', ') ?? '',
    extensions: definition.extensions.join(', '),
  }
}

function draftFromForm(form: FormState): BoundDefinitionDraft {
  const maxTokens = positiveInteger(form.maxTokens, 'maxTokens')
  const maxDepth = nonNegativeInteger(form.maxDepth, 'maxDepth')
  const hasAgentOptions = form.provider.trim() !== '' || form.model.trim() !== '' || maxTokens !== undefined
  const allow = splitNames(form.allow)
  const deny = splitNames(form.deny)
  const toolRestriction = form.toolMode === 'inherit' ? undefined : {
    ...(form.toolMode === 'allow' || form.toolMode === 'both' ? { allow } : {}),
    ...(form.toolMode === 'deny' || form.toolMode === 'both' ? { deny } : {}),
  }
  return {
    name: form.name.trim(),
    enabled: form.enabled,
    systemPrompt: form.systemPrompt,
    ...(hasAgentOptions ? { agentOptions: { provider: form.provider.trim(), model: form.model.trim(), ...(maxTokens === undefined ? {} : { maxTokens }) } } : {}),
    ...(maxDepth === undefined ? {} : { maxDepth }),
    ...(toolRestriction === undefined ? {} : { toolRestriction }),
    extensions: splitNames(form.extensions),
  }
}

function positiveInteger(rawValue: string, fieldName: string): number | undefined {
  if (rawValue.trim() === '') return undefined
  const value = Number(rawValue)
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`${fieldName} must be a positive integer`)
  return value
}

function nonNegativeInteger(rawValue: string, fieldName: string): number | undefined {
  if (rawValue.trim() === '') return undefined
  const value = Number(rawValue)
  if (!Number.isSafeInteger(value) || value < 0) throw new Error(`${fieldName} must be a non-negative integer`)
  return value
}

function splitNames(rawValue: string): string[] {
  return rawValue.split(/[\n,]/).map(value => value.trim()).filter(value => value !== '')
}
