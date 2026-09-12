import { useState } from 'react'
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

export function SettingsFields({ kind, value, onChange, configurations }: {
  kind: SettingsKind
  configurations?: string[]
  value: SettingsValue
  onChange: (value: SettingsValue) => void
}): React.JSX.Element {
  const { translate } = useI18n()
  if (kind === 'companion') {
    const inheritance = isRecord(value.inherit) ? value.inherit : {}
    return <>
      <Fields fields={[{ key: 'enabled', label: 'settings.enabled', type: 'boolean', required: true }]} value={value} onChange={onChange} prefix={kind} />
      <label className="settings-field">{translate('companion.configuration')}<select className="credential-input" value={String(value.configuration ?? '')} onChange={event => onChange(replaceField(value, 'configuration', event.target.value || undefined))}><option value="">{translate('settings.default')}</option>{configurations?.map(name => <option key={name}>{name}</option>)}</select></label>
      <Fields fields={[{ key: 'model', label: 'companion.inheritModel', type: 'boolean', required: true }, { key: 'prompt', label: 'companion.inheritPrompt', type: 'boolean', required: true }, { key: 'mcp', label: 'companion.inheritMCP', type: 'boolean', required: true }]} value={{ model: false, prompt: false, mcp: false, ...inheritance }} onChange={entry => onChange({ ...value, inherit: entry })} prefix="inherit" />
      <label className="settings-field">{translate('companion.inheritTools')}<select className="credential-input" value={String(inheritance.tools ?? 'none')} onChange={event => onChange({ ...value, inherit: { ...inheritance, tools: event.target.value } })}><option value="none">{translate('companion.none')}</option><option value="all">{translate('companion.all')}</option></select></label>
      <p className="settings-help">{translate('companion.templateHint')}</p>
    </>
  }
  if (kind === 'agent') {
    const prompts = isRecord(value.prompts) ? value.prompts : {}
    return <>
      <Fields fields={[{ key: 'provider', label: 'settings.provider', type: 'string' }, { key: 'model', label: 'companion.model', type: 'string' }, { key: 'maxTokens', label: 'companion.maxTokens', type: 'number', minimum: 1, integer: true }]} value={isRecord(value.model) ? value.model : {}} onChange={entry => onChange({ ...value, model: entry })} prefix="agent-model" />
      <Fields fields={promptFields} value={isRecord(prompts.default) ? prompts.default : {}} onChange={entry => onChange({ ...value, prompts: { ...prompts, default: entry } })} prefix="agent-prompt" />
      <NamedFields kind="tool" value={isRecord(value.tools) ? value.tools : {}} onChange={entry => onChange({ ...value, tools: entry })} />
      <NamedFields kind="mcp" value={isRecord(value.mcp) ? value.mcp : {}} onChange={entry => onChange({ ...value, mcp: entry })} />
      <p className="settings-help">{translate('companion.agentHint')}</p>
    </>
  }
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

function NamedFields({ kind, value, onChange }: { kind: 'tool' | 'mcp'; value: SettingsValue; onChange: (entry: SettingsValue) => void }): React.JSX.Element {
  const { translate } = useI18n()
  const [name, setName] = useState('')
  const add = (): void => {
    const key = name.trim()
    if (key === '' || Object.hasOwn(value, key)) return
    onChange({ ...value, [key]: kind === 'tool' ? {} : { enabled: true, connection: { serverName: key, transport: 'streamable-http', url: '' } } })
    setName('')
  }
  return <section className="settings-advanced">
    <h4>{translate(kind === 'tool' ? 'settings.tool' : 'settings.mcp')}</h4>
    {Object.entries(value).map(([key, entry]) => <details key={key} className="settings-advanced"><summary>{key}</summary>
      <SettingsFields kind={kind} value={isRecord(entry) ? entry : {}} onChange={updated => onChange({ ...value, [key]: updated })} />
      <button type="button" className="credential-remove" onClick={() => onChange(replaceField(value, key, undefined))}>{translate('companion.remove')}</button>
    </details>)}
    <label className="settings-field">{translate(kind === 'tool' ? 'companion.addTool' : 'companion.addMCP')}<input className="credential-input" value={name} onChange={event => setName(event.target.value)} /></label>
    <button type="button" className="credential-cancel" disabled={name.trim() === '' || Object.hasOwn(value, name.trim())} onClick={add}>{translate(kind === 'tool' ? 'companion.addTool' : 'companion.addMCP')}</button>
  </section>
}
