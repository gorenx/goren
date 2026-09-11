import { useI18n, type MessageKey } from '../i18n'
import type { SettingsKind, SettingsValue } from '../settings'
import { isRecord } from '../types'

interface SettingsField {
  key: string
  label: MessageKey
  type: 'string' | 'number' | 'boolean' | 'lines'
  required?: boolean
  multiline?: boolean
  minimum?: number
  maximum?: number
  integer?: boolean
}

const providerFields: SettingsField[] = [
  { key: 'apiKeyEnv', label: 'settings.credentialRef', type: 'string' },
  { key: 'baseURL', label: 'settings.baseURL', type: 'string' },
  { key: 'streamIdleTimeoutMs', label: 'settings.streamTimeout', type: 'number', minimum: 1, maximum: 2147483647 },
]
const promptFields: SettingsField[] = [
  { key: 'persona', label: 'settings.persona', type: 'string', multiline: true },
  { key: 'includeHarnessIdentity', label: 'settings.harnessIdentity', type: 'boolean' },
  { key: 'includeRuntimeContext', label: 'settings.runtimeContext', type: 'boolean' },
  { key: 'toolOrder', label: 'settings.toolOrder', type: 'lines' },
]
const toolFields: SettingsField[] = [
  { key: 'enabled', label: 'settings.enabled', type: 'boolean' },
  { key: 'visible', label: 'settings.visible', type: 'boolean' },
]
const mcpFields: SettingsField[] = [
  { key: 'url', label: 'mcp.url', type: 'string', required: true },
  { key: 'skipDiscover', label: 'settings.skipDiscover', type: 'boolean' },
  { key: 'requestTimeoutMs', label: 'settings.requestTimeout', type: 'number', minimum: 0, integer: true },
  { key: 'elicitationTimeoutMs', label: 'settings.elicitationTimeout', type: 'number', minimum: 0, integer: true },
  { key: 'maxInputRoundTrips', label: 'settings.maxInputRoundTrips', type: 'number', minimum: 0, integer: true },
  { key: 'maxTools', label: 'settings.maxTools', type: 'number', minimum: 0, integer: true },
  { key: 'maxPages', label: 'settings.maxPages', type: 'number', minimum: 0, integer: true },
  { key: 'toolResultLimit', label: 'settings.toolResultLimit', type: 'number', minimum: 0, integer: true },
]
const retryFields: SettingsField[] = [
  { key: 'maxRetries', label: 'settings.maxRetries', type: 'number', minimum: 0, integer: true },
  { key: 'retryableCodes', label: 'settings.retryableCodes', type: 'lines' },
]
const backoffFields: SettingsField[] = [
  { key: 'initialDelayMs', label: 'settings.initialDelay', type: 'number', minimum: 0 },
  { key: 'maxDelayMs', label: 'settings.maxDelay', type: 'number', minimum: 0 },
  { key: 'jitterRatio', label: 'settings.jitterRatio', type: 'number', minimum: 0, maximum: 1 },
]

// Removing an input changes the pending complete group only; it never reads a lower layer.
function replaceField(value: SettingsValue, key: string, entry: unknown): SettingsValue {
  const next = { ...value }
  if (entry === undefined) delete next[key]
  else next[key] = entry
  return next
}

function Fields({ fields, value, onChange, prefix }: {
  fields: SettingsField[]
  value: SettingsValue
  onChange: (value: SettingsValue) => void
  prefix: string
}): React.JSX.Element {
  const { translate } = useI18n()
  return <>{fields.map(field => {
    const current = value[field.key]
    const id = `settings-${prefix}-${field.key}`
    const change = (entry: unknown): void => onChange(replaceField(value, field.key, entry))
    return <div className="settings-field" key={field.key}>
      <label htmlFor={id}>{translate(field.label)}</label>
      {field.type === 'boolean' ? (
        <select id={id} className="credential-input" value={current === undefined ? '' : String(current)} onChange={event => change(event.target.value === '' ? undefined : event.target.value === 'true')}>
          {!field.required && <option value="">{translate('settings.default')}</option>}
          <option value="true">{translate('settings.yes')}</option><option value="false">{translate('settings.no')}</option>
        </select>
      ) : <>
        {!field.required && <label className="settings-default"><input type="checkbox" checked={current === undefined} onChange={event => change(event.target.checked ? undefined : field.type === 'number' ? field.minimum ?? 0 : field.type === 'lines' ? [] : '')} />{translate('settings.useDefault')}</label>}
        {field.multiline || field.type === 'lines'
          ? <textarea id={id} className="credential-input" rows={field.multiline ? 5 : 3} disabled={current === undefined} value={Array.isArray(current) ? current.join('\n') : String(current ?? '')} onChange={event => change(field.type === 'lines' ? event.target.value.split('\n') : event.target.value)} />
          : <input id={id} className="credential-input" type={field.type === 'number' ? 'number' : 'text'} required={field.required || field.type === 'number'} disabled={current === undefined && !field.required} min={field.minimum} max={field.maximum} step={field.integer ? 1 : 'any'} autoComplete="off" value={String(current ?? '')} onChange={event => change(field.type === 'number' ? (event.target.value === '' ? '' : Number(event.target.value)) : event.target.value)} />}
      </>}
    </div>
  })}</>
}

export function SettingsFields({ kind, value, onChange }: {
  kind: SettingsKind
  value: SettingsValue
  onChange: (value: SettingsValue) => void
}): React.JSX.Element {
  const { translate } = useI18n()
  if (kind === 'mcp') return <>
    <Fields fields={[{ key: 'enabled', label: 'settings.enabled', type: 'boolean', required: true }]} value={value} onChange={onChange} prefix={kind} />
    <Fields fields={mcpFields} value={isRecord(value.connection) ? value.connection : {}} onChange={entry => onChange({ ...value, connection: entry })} prefix="mcp-connection" />
  </>
  if (kind !== 'llmprovider') return <>
    <Fields fields={kind === 'prompt' ? promptFields : toolFields} value={value} onChange={onChange} prefix={kind} />
    <p className="settings-help">{translate(kind === 'prompt' ? 'settings.promptHint' : 'settings.toolHint')}</p>
  </>
  const retry = isRecord(value.retryPolicy) ? value.retryPolicy : undefined
  const backoff = retry !== undefined && isRecord(retry.backoff) ? retry.backoff : undefined
  const changeRetry = (entry: SettingsValue | undefined): void => onChange(replaceField(value, 'retryPolicy', entry))
  return <>
    <Fields fields={providerFields} value={value} onChange={onChange} prefix={kind} />
    <details className="settings-advanced">
      <summary>{translate('settings.retryPolicy')}</summary>
      <label className="settings-default"><input type="checkbox" checked={retry !== undefined} onChange={event => changeRetry(event.target.checked ? {} : undefined)} />{translate('settings.customRetry')}</label>
      {retry !== undefined && <>
        <label className="settings-field">{translate('settings.retryMode')}<select className="credential-input" value={String(retry.mode ?? '')} onChange={event => changeRetry(replaceField(retry, 'mode', event.target.value || undefined))}><option value="">{translate('settings.default')}</option><option value="normal">normal</option><option value="always">always</option></select></label>
        <Fields fields={retryFields} value={retry} onChange={changeRetry} prefix="retry" />
        <label className="settings-default"><input type="checkbox" checked={backoff !== undefined} onChange={event => changeRetry(replaceField(retry, 'backoff', event.target.checked ? {} : undefined))} />{translate('settings.customBackoff')}</label>
        {backoff !== undefined && <Fields fields={backoffFields} value={backoff} onChange={entry => changeRetry({ ...retry, backoff: entry })} prefix="backoff" />}
      </>}
    </details>
  </>
}
