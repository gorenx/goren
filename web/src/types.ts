export interface HostDescription {
  version: string
  cwd: string
  provider?: string
  model?: string
  attachedSessions: number
  canOpenPath: boolean
}

export interface SessionProjections {
  asOfSeq: number
  values: Record<string, unknown>
}

export interface SessionSummary {
  sessionId: string
  updatedAt: number
  running: boolean
  blank: boolean
  origin?: string
  cwd?: string
  agentPreset?: string
  projections?: SessionProjections
}

export interface SessionEvent {
  type: string
  seq: number
  time: number
  data?: unknown
}

export interface SessionListValue {
  items: SessionSummary[]
}

export interface SessionCreateValue {
  sessionId: string
}

export interface SessionHistoryValue {
  events: Array<{ event: SessionEvent }>
  hasMore: boolean
  projections?: SessionProjections
}

export interface StreamDraft {
  text: string
  reasoning: string
}

export interface MessageRow {
  role: 'assistant' | 'user'
  text: string
  reasoning: string
  streaming: boolean
  seq?: number
}

export interface ConversationSnapshot {
  phase: 'booting' | 'ready' | 'failed'
  host?: HostDescription
  sessions: readonly SessionSummary[]
  events: ReadonlyMap<string, readonly SessionEvent[]>
  streams: ReadonlyMap<string, StreamDraft>
  localTitles: ReadonlyMap<string, string>
  currentSessionId?: string
  onlineDownlinks: number
  composerState: string
  error?: string
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function recordString(source: Record<string, unknown>, key: string): string | undefined {
  const value = source[key]
  return typeof value === 'string' ? value : undefined
}

export function recordBoolean(source: Record<string, unknown>, key: string): boolean | undefined {
  const value = source[key]
  return typeof value === 'boolean' ? value : undefined
}
