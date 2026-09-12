import type { MessageKey } from './i18n'
import { isRecord } from './types'

export const companionPhases = {
  starting: 'companion.phase.starting',
  ready: 'companion.phase.ready',
  draining: 'companion.phase.draining',
  stopping: 'companion.phase.stopping',
  stopped: 'companion.phase.stopped',
  start_failed: 'companion.phase.start_failed',
  close_failed: 'companion.phase.close_failed',
} as const satisfies Record<string, MessageKey>

export type CompanionPhase = keyof typeof companionPhases

export interface Companion {
  parentSessionId: string
  name: string
  childSessionId?: string
  configuration?: string
  activity?: 'idle' | 'running' | 'unavailable'
  revision: number
  creationId?: string
  enabled: boolean
  removed: boolean
  persisted: boolean
  phase: CompanionPhase
  failure: boolean
}

export interface CompanionListing {
  phase: CompanionPhase
  failure: boolean
  items: Companion[]
}

export function decodeCompanions(raw: unknown, parent: string): CompanionListing {
  if (!isRecord(raw) || typeof raw.phase !== 'string' || !Object.hasOwn(companionPhases, raw.phase)
    || typeof raw.failure !== 'boolean' || !Array.isArray(raw.items)) throw new Error('Invalid Companion listing')
  const names = new Set<string>()
  for (const entry of raw.items) {
    if (!isRecord(entry) || entry.parentSessionId !== parent || typeof entry.name !== 'string' || entry.name === ''
      || names.has(entry.name) || typeof entry.revision !== 'number' || !Number.isSafeInteger(entry.revision) || entry.revision < 0
      || typeof entry.enabled !== 'boolean' || typeof entry.removed !== 'boolean' || typeof entry.persisted !== 'boolean'
      || typeof entry.failure !== 'boolean' || typeof entry.phase !== 'string' || !Object.hasOwn(companionPhases, entry.phase)
      || (entry.activity !== undefined && !['idle', 'running', 'unavailable'].includes(String(entry.activity)))
      || ['childSessionId', 'creationId', 'configuration'].some(key => entry[key] !== undefined && typeof entry[key] !== 'string')) throw new Error('Invalid Companion entry')
    names.add(entry.name)
  }
  return raw as unknown as CompanionListing
}

export function companionTransitioning(phase: CompanionPhase): boolean {
  return phase === 'starting' || phase === 'stopping' || phase === 'draining'
}
