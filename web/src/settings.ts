import type { MessageKey } from './i18n'
import { isRecord } from './types'

export type SettingsKind = 'llmprovider' | 'prompt' | 'tool' | 'mcp'
export type SettingsValue = Record<string, unknown>

export interface SettingsGroup {
  ns: SettingsKind
  name: string
  value: SettingsValue | null
  base?: SettingsValue | null
  user?: SettingsValue | null
  pendingRestart?: boolean
  revision: number
  applies: 'agent_restart' | 'restart'
}

export interface SettingsCatalog {
  writable: boolean
  namespaces: SettingsGroup[]
}

export const settingsMethods = {
  llmprovider: { update: 'settings.updateProvider', reset: 'settings.resetProvider' },
  prompt: { update: 'settings.updatePrompt', reset: 'settings.resetPrompt' },
  tool: { update: 'settings.updateTool', reset: 'settings.resetTool' },
  mcp: { update: 'settings.updateMCP', reset: 'settings.resetMCP' },
} as const

export const settingsLabels: Record<SettingsKind, MessageKey> = {
  llmprovider: 'settings.provider',
  prompt: 'settings.prompt',
  tool: 'settings.tool',
  mcp: 'settings.mcp',
}

// Identity contains both components, including names that contain separators.
export function settingsIdentity(group: Pick<SettingsGroup, 'ns' | 'name'>): string {
  return JSON.stringify([group.ns, group.name])
}

export function decodeSettingsGroup(raw: unknown): SettingsGroup {
  if (!isRecord(raw) || typeof raw.ns !== 'string' || !Object.hasOwn(settingsMethods, raw.ns)
    || typeof raw.name !== 'string' || raw.name.length === 0
    || typeof raw.revision !== 'number' || !Number.isSafeInteger(raw.revision) || raw.revision < 0
    || (raw.value !== null && !isRecord(raw.value))
    || (raw.applies !== 'agent_restart' && raw.applies !== 'restart')
    || (raw.base !== undefined && raw.base !== null && !isRecord(raw.base))
    || (raw.pendingRestart !== undefined && typeof raw.pendingRestart !== 'boolean')
    || (raw.user !== undefined && raw.user !== null && !isRecord(raw.user))) throw new Error('Invalid settings group')
  return raw as unknown as SettingsGroup
}

export function decodeSettingsCatalog(raw: unknown): SettingsCatalog {
  if (!isRecord(raw) || typeof raw.writable !== 'boolean' || !Array.isArray(raw.namespaces)) throw new Error('Invalid settings catalog')
  const namespaces = raw.namespaces.map(decodeSettingsGroup)
  if (new Set(namespaces.map(settingsIdentity)).size !== namespaces.length) throw new Error('Duplicate settings group')
  return { writable: raw.writable, namespaces }
}
