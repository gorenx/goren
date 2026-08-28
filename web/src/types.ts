import type { MessageKey } from './i18n'

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
  sourceEventSeqs?: number[]
}

export interface SessionListValue {
  items: SessionSummary[]
  nextCursor?: string
}

export interface SessionCreateValue {
  sessionId: string
}

export interface SessionHistoryValue {
  events: Array<{ event: SessionEvent }>
  hasMore: boolean
  projections?: SessionProjections
}

export interface SessionHistoryState {
  beforeSeq?: number
  hasMore: boolean
  loading: boolean
}

export interface StreamDraft {
  text: string
  reasoning: string
  streaming: boolean
  interrupted?: boolean
  seq?: number
}

export interface QuestionOption {
  label: string
  description?: string
}

export interface QuestionItem {
  id: string
  question: string
  header?: string
  detail?: string
  options?: readonly QuestionOption[]
  multiSelect?: boolean
}

export interface PendingQuestionRequest {
  rpcId: string
  sessionId: string
  questions: readonly QuestionItem[]
}

export interface QuestionAnswerItem {
  id: string
  selected: string[]
  custom?: string
}

export interface MessageRow {
  role: 'assistant' | 'user'
  text: string
  reasoning: string
  streaming: boolean
  interrupted?: boolean
  seq?: number
}

export interface CredentialView {
  configured: boolean
  source?: string
  writable: boolean
}

export interface CredentialsDescribeValue {
  credentials: Record<string, CredentialView>
}

export interface BoundAgentOptions {
  provider: string
  model: string
  maxTokens?: number
}

export interface BoundToolRestriction {
  allow?: string[]
  deny?: string[]
}

export interface BoundDefinitionDraft {
  name: string
  enabled: boolean
  systemPrompt: string
  agentOptions?: BoundAgentOptions
  maxDepth?: number
  toolRestriction?: BoundToolRestriction
  extensions: string[]
}

export interface BoundDefinition extends BoundDefinitionDraft {
  revision: number
}

export interface BoundListValue {
  definitions: BoundDefinition[]
}

export interface BoundDefinitionValue {
  definition: BoundDefinition
}

export interface ConversationSnapshot {
  phase: 'booting' | 'ready' | 'failed'
  host?: HostDescription
  sessions: readonly SessionSummary[]
  nextSessionCursor?: string
  loadingMoreSessions: boolean
  events: ReadonlyMap<string, readonly SessionEvent[]>
  histories: ReadonlyMap<string, SessionHistoryState>
  streams: ReadonlyMap<string, StreamDraft>
  pendingQuestions: ReadonlyMap<string, PendingQuestionRequest>
  localTitles: ReadonlyMap<string, string>
  currentSessionId?: string
  creatingSession: boolean
  onlineDownlinks: number
  composerState: MessageKey
  credentialLoaded: boolean
  credential?: CredentialView
  boundDefinitionsLoaded: boolean
  boundDefinitions: readonly BoundDefinition[]
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
