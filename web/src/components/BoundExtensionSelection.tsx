import { useState } from 'react'
import { useI18n } from '../i18n'
import type { BoundExtensionOption, ConversationSnapshot } from '../types'
import { BoundCatalogFailure } from './BoundCatalogFailure'

interface BoundExtensionSelectionProps {
  state: ConversationSnapshot['boundExtensionsState']
  error?: string
  options: readonly BoundExtensionOption[]
  selectedNames: readonly string[]
  disabled: boolean
  onAdd: (name: string) => void
  onRemove: (name: string) => void
  onMove: (name: string, offset: -1 | 1) => void
  onRetry: () => void
}

export function BoundExtensionSelection({ state, error, options, selectedNames, disabled, onAdd, onRemove, onMove, onRetry }: BoundExtensionSelectionProps): React.JSX.Element {
  const { translate } = useI18n()
  const [pickerOpen, setPickerOpen] = useState(false)
  const [query, setQuery] = useState('')
  const availableNames = new Set(options.map(option => option.name))
  const normalizedQuery = query.trim().toLocaleLowerCase()
  const choices = options.filter(option => !selectedNames.includes(option.name) && (normalizedQuery === '' || option.name.toLocaleLowerCase().includes(normalizedQuery)))
  const hasCatalog = state === 'ready' || options.length > 0
  const hasUnselectedOptions = options.some(option => !selectedNames.includes(option.name))

  return (
    <section className="bound-section" aria-labelledby="bound-extensions-title">
      <div className="bound-section-heading">
        <div><h3 id="bound-extensions-title">{translate('bound.extensions')}</h3><p>{translate('bound.extensionsDescription')}</p></div>
        <button type="button" className="selection-add" disabled={disabled || !hasCatalog || !hasUnselectedOptions} onClick={() => setPickerOpen(open => !open)}>{translate('bound.addExtension')}</button>
      </div>
      {state === 'loading' && <p className="bound-catalog-state">{translate('bound.extensionsLoading')}</p>}
      {state === 'failed' && <BoundCatalogFailure message={error} label={translate('bound.extensionsLoadFailed')} retryLabel={translate('bound.retry')} disabled={disabled} onRetry={onRetry} />}
      {state === 'ready' && options.length === 0 && <p className="bound-catalog-state">{translate('bound.extensionsCatalogEmpty')}</p>}
      {selectedNames.length === 0 ? <p className="selection-empty">{translate('bound.extensionsEmpty')}</p> : (
        <ol className="extension-stack">
          {selectedNames.map((name, index) => {
            const unavailable = state === 'ready' && !availableNames.has(name)
            return (
              <li key={name} data-unavailable={unavailable || undefined}>
                <span className="extension-order">{String(index + 1).padStart(2, '0')}</span>
                <code>{name}</code>
                <small>{unavailable ? translate('bound.extensionUnavailable') : ''}</small>
                <div>
                  <button type="button" disabled={disabled || index === 0} aria-label={translate('bound.moveExtensionUp', { name })} onClick={() => onMove(name, -1)}>↑</button>
                  <button type="button" disabled={disabled || index === selectedNames.length - 1} aria-label={translate('bound.moveExtensionDown', { name })} onClick={() => onMove(name, 1)}>↓</button>
                  <button type="button" disabled={disabled} aria-label={translate('bound.removeExtension', { name })} onClick={() => onRemove(name)}>×</button>
                </div>
              </li>
            )
          })}
        </ol>
      )}
      {pickerOpen && hasCatalog && (
        <div className="catalog-picker extension-picker">
          <div className="catalog-picker-search">
            <input type="search" value={query} autoFocus aria-label={translate('bound.extensionsSearch')} placeholder={translate('bound.extensionsSearch')} onChange={event => setQuery(event.target.value)} />
            <button type="button" onClick={() => setPickerOpen(false)}>{translate('bound.done')}</button>
          </div>
          <div className="extension-picker-list">
            {choices.map(option => <button type="button" key={option.name} onClick={() => onAdd(option.name)}><span>+</span><code>{option.name}</code></button>)}
            {choices.length === 0 && <p>{options.length === selectedNames.length ? translate('bound.allExtensionsSelected') : translate('bound.noMatchingExtensions')}</p>}
          </div>
        </div>
      )}
    </section>
  )
}
