import { useState } from 'react'
import { useI18n } from '../i18n'
import type { BoundToolOption, ConversationSnapshot } from '../types'
import { BoundCatalogFailure } from './BoundCatalogFailure'

export type BoundToolMode = 'inherit' | 'allow' | 'deny'

interface BoundToolSelectionProps {
  mode: BoundToolMode
  state: ConversationSnapshot['boundToolsState']
  error?: string
  options: readonly BoundToolOption[]
  allowedNames: readonly string[]
  deniedNames: readonly string[]
  disabled: boolean
  onModeChange: (mode: BoundToolMode) => void
  onToggle: (name: string, selected: boolean) => void
  onRetry: () => void
}

export function BoundToolSelection({ mode, state, error, options, allowedNames, deniedNames, disabled, onModeChange, onToggle, onRetry }: BoundToolSelectionProps): React.JSX.Element {
  const { translate } = useI18n()
  const [pickerOpen, setPickerOpen] = useState(false)
  const [query, setQuery] = useState('')
  const selectedNames = mode === 'deny' ? deniedNames : allowedNames
  const selected = new Set(selectedNames)
  const availableNames = new Set(options.map(option => option.name))
  const selectedOptions = selectedNames.map(name => options.find(option => option.name === name) ?? {
    name,
    description: translate('bound.toolUnavailable'),
  })
  const normalizedQuery = query.trim().toLocaleLowerCase()
  const filteredOptions = options.filter(option => normalizedQuery === '' || `${option.name} ${option.description}`.toLocaleLowerCase().includes(normalizedQuery))
  const hasCatalog = state === 'ready' || options.length > 0

  return (
    <section className="bound-section" aria-labelledby="bound-tools-title">
      <div className="bound-section-heading"><div><h3 id="bound-tools-title">{translate('bound.tools')}</h3><p>{translate('bound.toolsDescription')}</p></div></div>
      <div className="tool-policy" role="radiogroup" aria-label={translate('bound.toolPolicy')}>
        {(['inherit', 'allow', 'deny'] as const).map(policy => (
          <label key={policy} data-selected={mode === policy || undefined}>
            <input type="radio" name="toolMode" value={policy} checked={mode === policy} disabled={disabled} onChange={() => onModeChange(policy)} />
            <span>{translate(policy === 'inherit' ? 'bound.toolsInherit' : policy === 'allow' ? 'bound.toolsAllow' : 'bound.toolsDeny')}</span>
          </label>
        ))}
      </div>
      {state === 'loading' && <p className="bound-catalog-state">{translate('bound.toolsLoading')}</p>}
      {state === 'failed' && <BoundCatalogFailure message={error} label={translate('bound.toolsLoadFailed')} retryLabel={translate('bound.retry')} disabled={disabled} onRetry={onRetry} />}
      {mode === 'inherit' ? (
        <p className="tool-inherit-summary">{hasCatalog ? translate('bound.toolsInheritedCount', { count: options.length }) : translate('bound.toolsInheritPending')}</p>
      ) : (
        <div className="selection-control">
          {selectedOptions.length === 0 ? <p className="selection-empty">{translate(mode === 'allow' ? 'bound.toolsAllowEmpty' : 'bound.toolsDenyEmpty')}</p> : (
            <div className="tool-chips">
              {selectedOptions.map(option => (
                <span className="tool-chip" key={option.name} data-unavailable={!availableNames.has(option.name) && state === 'ready' || undefined}>
                  <code>{option.name}</code>
                  {!availableNames.has(option.name) && state === 'ready' && <span className="tool-unavailable-mark" title={translate('bound.toolUnavailable')}>!</span>}
                  <button type="button" disabled={disabled} aria-label={translate('bound.removeTool', { name: option.name })} onClick={() => onToggle(option.name, false)}>×</button>
                </span>
              ))}
            </div>
          )}
          <button type="button" className="selection-add" disabled={disabled || !hasCatalog} onClick={() => setPickerOpen(open => !open)}>{translate(mode === 'allow' ? 'bound.chooseTools' : 'bound.chooseExcludedTools')}</button>
          {pickerOpen && hasCatalog && (
            <div className="catalog-picker">
              <div className="catalog-picker-search">
                <input type="search" value={query} autoFocus aria-label={translate('bound.toolsSearch')} placeholder={translate('bound.toolsSearch')} onChange={event => setQuery(event.target.value)} />
                <button type="button" onClick={() => setPickerOpen(false)}>{translate('bound.done')}</button>
              </div>
              <div className="catalog-picker-list">
                {filteredOptions.map(option => (
                  <label key={option.name}>
                    <input type="checkbox" name={mode} value={option.name} checked={selected.has(option.name)} disabled={disabled} onChange={event => onToggle(option.name, event.target.checked)} />
                    <span><code>{option.name}</code>{option.description !== '' && <small>{option.description}</small>}</span>
                  </label>
                ))}
                {filteredOptions.length === 0 && <p>{translate('bound.noMatchingTools')}</p>}
              </div>
            </div>
          )}
        </div>
      )}
    </section>
  )
}
