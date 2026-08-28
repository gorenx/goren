import { useEffect, useMemo, useState } from 'react'
import type { ConversationStore } from '../conversation-store'
import { CloseIcon } from '../icons'
import { useI18n } from '../i18n'
import type { BoundAgentOptions, BoundDefinition, BoundDefinitionDraft, ConversationSnapshot } from '../types'
import { BoundExtensionSelection } from './BoundExtensionSelection'
import { BoundToolSelection, type BoundToolMode } from './BoundToolSelection'

interface BoundDefinitionDialogProps {
  store: ConversationStore
  snapshot: ConversationSnapshot
  onClose: () => void
}

interface FormState {
  name: string
  revision?: number
  enabled: boolean
  systemPrompt: string
  agentOptions?: BoundAgentOptions
  maxDepth?: number
  toolMode: BoundToolMode
  allow: string[]
  deny: string[]
  extensions: string[]
}

function newForm(): FormState {
  return {
    name: '',
    enabled: true,
    systemPrompt: '',
    toolMode: 'inherit',
    allow: [],
    deny: [],
    extensions: [],
  }
}

export function BoundDefinitionDialog({ store, snapshot, onClose }: BoundDefinitionDialogProps): React.JSX.Element {
  const { translate } = useI18n()
  const [form, setForm] = useState<FormState>(newForm)
  const [baseline, setBaseline] = useState<FormState>(newForm)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string>()
  const editing = form.revision !== undefined
  const dirty = !sameForm(form, baseline)
  const selectedDefinition = useMemo(
    () => snapshot.boundDefinitions.find(definition => definition.name === form.name),
    [snapshot.boundDefinitions, form.name],
  )

  useEffect(() => {
    void store.refreshBoundTools()
    void store.refreshBoundExtensions()
  }, [store])

  const confirmDiscard = (): boolean => !dirty || globalThis.confirm(translate('bound.discardConfirm'))

  const requestClose = (): void => {
    if (confirmDiscard()) onClose()
  }

  const choose = (definition: BoundDefinition): void => {
    if (!confirmDiscard()) return
    const nextForm = formFromDefinition(definition)
    setForm(nextForm)
    setBaseline(nextForm)
    setFailure(undefined)
  }

  const startNew = (): void => {
    if (!confirmDiscard()) return
    const nextForm = newForm()
    setForm(nextForm)
    setBaseline(nextForm)
    setFailure(undefined)
  }

  const submit = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    const draft = draftFromForm(form)
    setBusy(true)
    setFailure(undefined)
    try {
      const committed = form.revision === undefined
        ? await store.createBoundDefinition(draft)
        : await store.replaceBoundDefinition(form.revision, draft)
      const committedForm = formFromDefinition(committed)
      setForm(committedForm)
      setBaseline(committedForm)
    } catch (error) {
      setFailure(error instanceof Error ? error.message : String(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="credential-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) requestClose() }}>
      <section className="definition-dialog" role="dialog" aria-modal="true" aria-labelledby="bound-definition-title">
        <header className="definition-dialog-header">
          <div><h2 id="bound-definition-title">{translate('bound.title')}</h2><p>{translate('bound.description')}</p></div>
          <button type="button" className="icon-button" aria-label={translate('bound.close')} onClick={requestClose}><CloseIcon size={16} /></button>
        </header>

        <div className="definition-layout">
          <aside className="definition-list" aria-label={translate('bound.list')}>
            <button type="button" className="definition-new" onClick={startNew}>{translate('bound.new')}</button>
            {snapshot.boundDefinitions.map(definition => (
              <button type="button" key={definition.name} className="definition-item" aria-current={selectedDefinition?.name === definition.name} onClick={() => choose(definition)}>
                <span>{definition.name}</span>
                <small>{definition.enabled ? translate('bound.enabled') : translate('bound.disabled')} · r{definition.revision}</small>
              </button>
            ))}
            {snapshot.boundDefinitionsLoaded && snapshot.boundDefinitions.length === 0 && <p className="definition-list-empty">{translate('bound.empty')}</p>}
          </aside>

          <form className="definition-form" onSubmit={event => void submit(event)}>
            <div className="definition-form-body">
              <div className="definition-identity">
                <label className="definition-name">{translate('bound.name')}<input name="name" className="credential-input" value={form.name} disabled={busy || editing} autoFocus={!editing} onChange={event => setForm(current => ({ ...current, name: event.target.value }))} /></label>
                <label className="definition-enabled"><input type="checkbox" checked={form.enabled} disabled={busy} onChange={event => setForm(current => ({ ...current, enabled: event.target.checked }))} /><span>{translate(form.enabled ? 'bound.enabled' : 'bound.disabled')}</span></label>
              </div>

              <label className="definition-prompt-label">{translate('bound.systemPrompt')}<textarea name="systemPrompt" className="credential-input definition-prompt" value={form.systemPrompt} disabled={busy} placeholder={translate('bound.systemPromptPlaceholder')} onChange={event => setForm(current => ({ ...current, systemPrompt: event.target.value }))} /></label>

              <BoundToolSelection
                mode={form.toolMode}
                state={snapshot.boundToolsState}
                error={snapshot.boundToolsError}
                options={snapshot.boundTools}
                allowedNames={form.allow}
                deniedNames={form.deny}
                disabled={busy}
                onModeChange={toolMode => setForm(current => ({ ...current, toolMode }))}
                onToggle={(name, selected) => setForm(current => {
                  const fieldName = current.toolMode === 'deny' ? 'deny' : 'allow'
                  return { ...current, [fieldName]: toggleName(current[fieldName], name, selected) }
                })}
                onRetry={() => void store.refreshBoundTools()}
              />

              <BoundExtensionSelection
                state={snapshot.boundExtensionsState}
                error={snapshot.boundExtensionsError}
                options={snapshot.boundExtensions}
                selectedNames={form.extensions}
                disabled={busy}
                onAdd={name => setForm(current => ({ ...current, extensions: current.extensions.includes(name) ? current.extensions : [...current.extensions, name] }))}
                onRemove={name => setForm(current => ({ ...current, extensions: current.extensions.filter(currentName => currentName !== name) }))}
                onMove={(name, offset) => setForm(current => ({ ...current, extensions: moveName(current.extensions, name, offset) }))}
                onRetry={() => void store.refreshBoundExtensions()}
              />
            </div>

            <footer className="definition-footer">
              <div>{failure !== undefined && <p role="alert">{failure}</p>}</div>
              <div className="definition-footer-actions">
                <button type="button" className="credential-cancel" disabled={busy} onClick={requestClose}>{translate('bound.cancel')}</button>
                <button type="submit" className="credential-save" disabled={busy || form.name.trim() === '' || form.systemPrompt.trim() === '' || !dirty}>{busy ? translate('bound.saving') : editing ? translate('bound.saveChanges') : translate('bound.create')}</button>
              </div>
            </footer>
          </form>
        </div>
      </section>
    </div>
  )
}

function formFromDefinition(definition: BoundDefinition): FormState {
  const allowedNames = definition.toolRestriction?.allow
  const deniedNames = definition.toolRestriction?.deny
  if (allowedNames !== undefined) {
    const denied = new Set(deniedNames ?? [])
    return { ...baseFormFromDefinition(definition), toolMode: 'allow', allow: allowedNames.filter(name => !denied.has(name)), deny: [] }
  }
  if (deniedNames !== undefined) {
    return { ...baseFormFromDefinition(definition), toolMode: 'deny', allow: [], deny: [...deniedNames] }
  }
  return baseFormFromDefinition(definition)
}

function baseFormFromDefinition(definition: BoundDefinition): FormState {
  return {
    name: definition.name,
    revision: definition.revision,
    enabled: definition.enabled,
    systemPrompt: definition.systemPrompt,
    agentOptions: definition.agentOptions === undefined ? undefined : { ...definition.agentOptions },
    maxDepth: definition.maxDepth,
    toolMode: 'inherit',
    allow: [],
    deny: [],
    extensions: [...definition.extensions],
  }
}

function draftFromForm(form: FormState): BoundDefinitionDraft {
  const toolRestriction = form.toolMode === 'inherit' ? undefined : form.toolMode === 'allow' ? { allow: [...form.allow] } : { deny: [...form.deny] }
  return {
    name: form.name.trim(),
    enabled: form.enabled,
    systemPrompt: form.systemPrompt,
    ...(form.agentOptions === undefined ? {} : { agentOptions: { ...form.agentOptions } }),
    ...(form.maxDepth === undefined ? {} : { maxDepth: form.maxDepth }),
    ...(toolRestriction === undefined ? {} : { toolRestriction }),
    extensions: [...form.extensions],
  }
}

function toggleName(names: readonly string[], name: string, selected: boolean): string[] {
  if (selected) return names.includes(name) ? [...names] : [...names, name]
  return names.filter(currentName => currentName !== name)
}

function moveName(names: readonly string[], name: string, offset: -1 | 1): string[] {
  const currentIndex = names.indexOf(name)
  const nextIndex = currentIndex + offset
  if (currentIndex < 0 || nextIndex < 0 || nextIndex >= names.length) return [...names]
  const moved = [...names]
  ;[moved[currentIndex], moved[nextIndex]] = [moved[nextIndex], moved[currentIndex]]
  return moved
}

function sameForm(left: FormState, right: FormState): boolean {
  return JSON.stringify(left) === JSON.stringify(right)
}
