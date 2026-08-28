import { HarnessAPI } from './api'
import type {
  ConversationSnapshot,
  BoundDefinition,
  BoundDefinitionDraft,
  BoundDefinitionValue,
  BoundExtensionsValue,
  BoundListValue,
  BoundToolsValue,
  CredentialsDescribeValue,
  HostDescription,
  MessageRow,
  PendingQuestionRequest,
  QuestionAnswerItem,
  QuestionItem,
  QuestionOption,
  SessionCreateValue,
  SessionEvent,
  SessionHistoryValue,
  SessionHistoryState,
  SessionListValue,
  SessionSummary,
  StreamDraft,
} from './types'
import { isRecord, recordBoolean, recordString } from './types'
import type { Locale, MessageKey, Translator } from './i18n'
import { mergeEvents } from './session-events'
import { projectStream } from './session-stream'

type Subscriber = () => void

const initialSnapshot: ConversationSnapshot = {
  phase: 'booting',
  sessions: [],
  loadingMoreSessions: false,
  events: new Map(),
  histories: new Map(),
  streams: new Map(),
  pendingQuestions: new Map(),
  localTitles: new Map(),
  creatingSession: false,
  onlineDownlinks: 0,
  composerState: 'composer.connecting',
  credentialLoaded: false,
  boundDefinitionsLoaded: false,
  boundDefinitions: [],
  boundToolsState: 'idle',
  boundTools: [],
  boundExtensionsState: 'idle',
  boundExtensions: [],
}

const historyPageMessages = 20
const sessionListPageSize = 50

export const DEEPSEEK_CREDENTIAL_REF = 'DEEPSEEK_API_KEY'

export class ConversationStore {
  readonly #subscribers = new Set<Subscriber>()
  readonly #api: HarnessAPI
  readonly #translateText: Translator
  readonly #cancellingSessions = new Set<string>()
  #value = initialSnapshot
  #selectionVersion = 0
  #sessionListVersion = 0
  #toastTimer?: number
  #started = false

  constructor(translateText: Translator) {
    this.#translateText = translateText
    this.#api = new HarnessAPI(
      request => this.#receiveMux(request.rpcId, request.payload),
      request => this.#receiveHost(request.payload),
      count => this.#patch({ onlineDownlinks: count }),
      translateText,
    )
  }

  readonly snapshot = (): ConversationSnapshot => this.#value

  readonly subscribe = (subscriber: Subscriber): (() => void) => {
    this.#subscribers.add(subscriber)
    return () => this.#subscribers.delete(subscriber)
  }

  async start(): Promise<void> {
    if (this.#started) return
    this.#started = true
    try {
      const host = await this.#api.call<HostDescription>('host.describe', {})
      this.#patch({ host, composerState: 'composer.syncingSessions' })
      await Promise.all([
        this.refreshCredential(),
        this.refreshBoundDefinitions(),
      ])
      this.#api.connect()
      await this.refreshSessions()
      if (this.#value.sessions.length === 0) await this.createSession()
      else await this.selectSession(this.#value.sessions[0].sessionId)
      this.#patch({ phase: 'ready' })
    } catch (error) {
      this.#fail(error, true)
    }
  }

  dispose(): void {
    this.#api.close()
    globalThis.clearTimeout(this.#toastTimer)
  }

  async refreshSessions(): Promise<void> {
    const listVersion = ++this.#sessionListVersion
    this.#patch({ loadingMoreSessions: false })
    const result = await this.#api.call<SessionListValue>('session.list', {
      limit: sessionListPageSize,
    })
    if (listVersion !== this.#sessionListVersion) return
    const sessions = [...(result.items ?? [])]
      .filter(item => item.origin !== 'subagent')
    this.#patch({
      sessions,
      nextSessionCursor: result.nextCursor,
      loadingMoreSessions: false,
    })
  }

  async loadMoreSessions(): Promise<void> {
    const cursor = this.#value.nextSessionCursor
    if (cursor === undefined || this.#value.loadingMoreSessions) return
    const listVersion = this.#sessionListVersion
    this.#patch({ loadingMoreSessions: true })
    try {
      const result = await this.#api.call<SessionListValue>('session.list', {
        cursor,
        limit: sessionListPageSize,
      })
      if (listVersion !== this.#sessionListVersion) return
      const appended = (result.items ?? []).filter(item => item.origin !== 'subagent')
      this.#patch({
        sessions: appendSessions(this.#value.sessions, appended),
        nextSessionCursor: result.nextCursor,
        loadingMoreSessions: false,
      })
    } catch (error) {
      if (listVersion === this.#sessionListVersion) {
        this.#patch({ loadingMoreSessions: false })
        this.#fail(error)
      }
    }
  }

  async refreshCredential(): Promise<void> {
    const result = await this.#api.call<CredentialsDescribeValue>('credentials.describe', {
      refs: [DEEPSEEK_CREDENTIAL_REF],
    })
    this.#patch({ credential: result.credentials[DEEPSEEK_CREDENTIAL_REF], credentialLoaded: true })
  }

  async saveCredential(value: string): Promise<void> {
    try {
      await this.#api.call<unknown>('credentials.set', { ref: DEEPSEEK_CREDENTIAL_REF, value })
      await this.refreshCredential()
    } catch (error) {
    this.#fail(error)
    throw error
  }
  }

  async unsetCredential(): Promise<void> {
    try {
      await this.#api.call<unknown>('credentials.unset', { ref: DEEPSEEK_CREDENTIAL_REF })
      await this.refreshCredential()
    } catch (error) {
      this.#fail(error)
      throw error
  }
  }

  async refreshBoundDefinitions(): Promise<void> {
    const result = await this.#api.call<BoundListValue>('bound.list', {})
    this.#patch({
      boundDefinitions: result.definitions ?? [],
      boundDefinitionsLoaded: true,
    })
  }

  async refreshBoundTools(): Promise<void> {
    if (this.#value.boundToolsState === 'loading') return
    this.#patch({
      boundToolsState: 'loading',
      boundToolsError: undefined,
    })
    try {
      const result = await this.#api.call<BoundToolsValue>('bound.tools', {})
      this.#patch({
        boundTools: result.tools ?? [],
        boundToolsState: 'ready',
        boundToolsError: undefined,
      })
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      this.#patch({
        boundToolsState: 'failed',
        boundToolsError: message,
      })
      console.error(error)
    }
  }

  async refreshBoundExtensions(): Promise<void> {
    if (this.#value.boundExtensionsState === 'loading') return
    this.#patch({
      boundExtensionsState: 'loading',
      boundExtensionsError: undefined,
    })
    try {
      const result = await this.#api.call<BoundExtensionsValue>('bound.extensions', {})
      this.#patch({
        boundExtensions: result.extensions ?? [],
        boundExtensionsState: 'ready',
        boundExtensionsError: undefined,
      })
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      this.#patch({
        boundExtensionsState: 'failed',
        boundExtensionsError: message,
      })
      console.error(error)
    }
  }

  async createBoundDefinition(draft: BoundDefinitionDraft): Promise<BoundDefinition> {
    try {
      const result = await this.#api.call<BoundDefinitionValue>('bound.create', {
        definition: draft,
      })
      await this.refreshBoundDefinitions()
      return result.definition
    } catch (error) {
      this.#fail(error)
      throw error
    }
  }

  async replaceBoundDefinition(
    expectedRevision: number,
    draft: BoundDefinitionDraft,
  ): Promise<BoundDefinition> {
    try {
      const result = await this.#api.call<BoundDefinitionValue>('bound.replace', {
        expectedRevision,
        definition: draft,
      })
      await this.refreshBoundDefinitions()
      return result.definition
    } catch (error) {
      this.#fail(error)
      throw error
    }
  }

  async createSession(): Promise<void> {
    if (this.#value.creatingSession) return
    const previousSessionId = this.#value.currentSessionId
    const previousComposerState = this.#value.composerState
    const creationSelectionVersion = ++this.#selectionVersion
    this.#patch({
      currentSessionId: undefined,
      creatingSession: true,
      composerState: 'composer.creatingSession',
    })
    let created: SessionCreateValue
    try {
      const payload = this.#value.host?.cwd ? { cwd: this.#value.host.cwd } : {}
      created = await this.#api.call<SessionCreateValue>('session.create', payload)
    } catch (error) {
      if (this.#selectionVersion === creationSelectionVersion) {
        this.#patch({
          currentSessionId: previousSessionId,
          composerState: previousComposerState,
        })
      }
      this.#patch({ creatingSession: false })
      this.#fail(error)
      return
    }
    const sessions = prependSession(this.#value.sessions, {
      sessionId: created.sessionId,
      updatedAt: Date.now(),
      running: false,
      blank: true,
      cwd: this.#value.host?.cwd,
    })
    this.#patch({ sessions, creatingSession: false })
    if (this.#selectionVersion === creationSelectionVersion) {
      const selectionVersion = this.#beginSessionSelection(created.sessionId, 'composer.ready')
      const select = this.#readSelectedSession(created.sessionId, selectionVersion)
      const refresh = this.refreshSessions().catch(error => this.#fail(error))
      await Promise.all([select, refresh])
    } else {
      await this.refreshSessions().catch(error => this.#fail(error))
    }
  }

  async selectSession(sessionId: string): Promise<void> {
    const selectionVersion = this.#beginSessionSelection(sessionId, 'composer.readingHistory')
    await this.#readSelectedSession(sessionId, selectionVersion)
  }

  #beginSessionSelection(sessionId: string, composerState: MessageKey): number {
    const selectionVersion = ++this.#selectionVersion
    const streams = new Map(this.#value.streams)
    streams.delete(sessionId)
    this.#patch({ currentSessionId: sessionId, streams, composerState })
    return selectionVersion
  }

  async #readSelectedSession(sessionId: string, selectionVersion: number): Promise<void> {
    try {
      const history = await this.#api.call<SessionHistoryValue>('session.history', {
        sessionId,
        maxMessages: historyPageMessages,
      })
      if (selectionVersion !== this.#selectionVersion) return
      const historyEvents = (history.events ?? []).map(entry => entry.event)
      const events = new Map(this.#value.events)
      const sessionEvents = compactCompletedChunks(mergeEvents(
        historyEvents,
        this.#value.events.get(sessionId) ?? [],
      ))
      events.set(sessionId, sessionEvents)
      const histories = new Map(this.#value.histories)
      histories.set(sessionId, historyState(historyEvents, history.hasMore))
      const streams = new Map(this.#value.streams)
      setProjectedStream(streams, sessionId, sessionEvents)
      this.#patch({
        events,
        histories,
        streams,
        composerState: this.currentSession()?.running ? 'composer.agentWorking' : 'composer.ready',
      })
    } catch (error) {
      if (selectionVersion === this.#selectionVersion) this.#fail(error)
    }
  }

  async loadOlderHistory(): Promise<void> {
    const sessionId = this.#value.currentSessionId
    if (sessionId === undefined) return
    const current = this.#value.histories.get(sessionId)
    if (current === undefined || !current.hasMore || current.loading || current.beforeSeq === undefined) return
    const selectionVersion = this.#selectionVersion
    const histories = new Map(this.#value.histories)
    histories.set(sessionId, { ...current, loading: true })
    this.#patch({ histories })
    try {
      const history = await this.#api.call<SessionHistoryValue>('session.history', {
        sessionId,
        beforeSeq: current.beforeSeq,
        maxMessages: historyPageMessages,
      })
      if (selectionVersion !== this.#selectionVersion || this.#value.currentSessionId !== sessionId) return
      const olderEvents = (history.events ?? []).map(entry => entry.event)
      const events = new Map(this.#value.events)
      const sessionEvents = compactCompletedChunks(mergeEvents(
        olderEvents,
        events.get(sessionId) ?? [],
      ))
      events.set(sessionId, sessionEvents)
      const nextHistories = new Map(this.#value.histories)
      nextHistories.set(sessionId, historyState(
        olderEvents,
        olderEvents.length === 0 ? false : history.hasMore,
        current.beforeSeq,
      ))
      const streams = new Map(this.#value.streams)
      setProjectedStream(streams, sessionId, sessionEvents)
      this.#patch({ events, histories: nextHistories, streams })
    } catch (error) {
      if (selectionVersion !== this.#selectionVersion || this.#value.currentSessionId !== sessionId) return
      const failedHistories = new Map(this.#value.histories)
      failedHistories.set(sessionId, { ...current, loading: false })
      this.#patch({ histories: failedHistories })
      this.#fail(error)
    }
  }

  async submitPrompt(rawText: string): Promise<void> {
    const text = rawText.trim()
    const sessionId = this.#value.currentSessionId
    if (text === '' || sessionId === undefined) return
    const localTitles = new Map(this.#value.localTitles)
    localTitles.set(sessionId, text.slice(0, 52))
    this.#patch({ localTitles, composerState: 'composer.queued' })
    try {
      await this.#api.call<unknown>('session.prompt', {
        sessionId,
        mode: 'queue',
        content: [{ type: 'text', text }],
        clientTimeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
      })
    } catch (error) {
      this.#patch({ composerState: 'composer.sendFailed' })
      this.#fail(error)
      throw error
    }
  }

  async cancelCurrentTurn(): Promise<void> {
    const current = this.currentSession()
    if (current === undefined || !current.running || this.#cancellingSessions.has(current.sessionId)) return
    const sessionId = current.sessionId
    this.#cancellingSessions.add(sessionId)
    this.#patch({ composerState: 'composer.cancelling' })
    try {
      await this.#api.call<unknown>('session.cancel', { sessionId })
      if (this.currentSession()?.sessionId === sessionId && this.currentSession()?.running === true) {
        this.#patch({ composerState: 'composer.cancelRequested' })
      }
    } catch (error) {
      if (this.currentSession()?.sessionId === sessionId && this.currentSession()?.running === true) {
        this.#patch({ composerState: 'composer.agentWorking' })
      }
      this.#fail(error)
      throw error
    } finally {
      this.#cancellingSessions.delete(sessionId)
    }
  }

  currentSession(): SessionSummary | undefined {
    return this.#value.sessions.find(item => item.sessionId === this.#value.currentSessionId)
  }

  currentEvents(): readonly SessionEvent[] {
    const sessionId = this.#value.currentSessionId
    return sessionId === undefined ? [] : this.#value.events.get(sessionId) ?? []
  }

  currentHistory(): SessionHistoryState | undefined {
    const sessionId = this.#value.currentSessionId
    return sessionId === undefined ? undefined : this.#value.histories.get(sessionId)
  }

  currentMessages(): MessageRow[] {
    const rows: MessageRow[] = []
    for (const event of this.currentEvents()) {
      const row = messageFromEvent(event)
      if (row !== undefined) rows.push(row)
    }
    const sessionId = this.#value.currentSessionId
    const draft = sessionId === undefined ? undefined : this.#value.streams.get(sessionId)
    if (draft !== undefined && (draft.text !== '' || draft.reasoning !== '')) {
      rows.push({ role: 'assistant', ...draft })
    }
    rows.sort((left, right) => (left.seq ?? Number.MAX_SAFE_INTEGER) - (right.seq ?? Number.MAX_SAFE_INTEGER))
    return rows
  }

  currentQuestion(): PendingQuestionRequest | undefined {
    const sessionId = this.#value.currentSessionId
    if (sessionId === undefined) return undefined
    return [...this.#value.pendingQuestions.values()].find(request => request.sessionId === sessionId)
  }

  async answerQuestion(request: PendingQuestionRequest, answers: QuestionAnswerItem[]): Promise<void> {
    if (!this.#value.pendingQuestions.has(request.rpcId)) throw new Error(this.#translateText('error.questionEnded'))
    try {
      await this.#api.respond(request.rpcId, {
        ok: true,
        value: { sessionId: request.sessionId, answer: { answers } },
      })
      this.#removeQuestion(request.rpcId, 'composer.agentContinuing')
    } catch (error) {
      this.#fail(error)
      throw error
    }
  }

  async cancelQuestion(request: PendingQuestionRequest): Promise<void> {
    if (!this.#value.pendingQuestions.has(request.rpcId)) return
    try {
      await this.#api.respond(request.rpcId, {
        ok: false,
        error: { code: 'cancelled', message: 'cancelled by user', details: {} },
      })
      this.#removeQuestion(request.rpcId, 'composer.questionCancelled')
    } catch (error) {
      this.#fail(error)
      throw error
    }
  }

  sessionTitle(summary: SessionSummary): string {
    const local = this.#value.localTitles.get(summary.sessionId)
    const projected = summary.projections?.values.title
    if (local !== undefined) return local
    if (typeof projected === 'string' && projected.trim() !== '') return projected
    return summary.blank
      ? this.#translateText('conversation.new')
      : this.#translateText('session.fallback', { id: summary.sessionId.slice(0, 8) })
  }

  relativeTime(timestamp: number, activeLanguage: Locale): string {
    const elapsed = Math.max(0, Date.now() - timestamp)
    if (elapsed < 60_000) return this.#translateText('time.justNow')
    if (elapsed < 3_600_000) return this.#translateText('time.minutesAgo', { count: Math.floor(elapsed / 60_000) })
    if (elapsed < 86_400_000) return this.#translateText('time.hoursAgo', { count: Math.floor(elapsed / 3_600_000) })
    return new Date(timestamp).toLocaleDateString(activeLanguage, { month: 'short', day: 'numeric' })
  }

  dismissError(): void {
    this.#patch({ error: undefined })
  }

  #receiveMux(rpcId: string, payload: unknown): void {
    if (!isRecord(payload)) return
    const frameType = recordString(payload, 'type')
    if (frameType === 'stream/error') {
      const error = isRecord(payload.error) ? recordString(payload.error, 'message') : undefined
      this.#fail(new Error(error ?? this.#translateText('error.streamDisconnected')))
      return
    }
    if (frameType === 'question/requested') {
      const request = readQuestionRequest(rpcId, payload)
      if (request === undefined) {
        this.#fail(new Error(this.#translateText('error.invalidQuestion')))
        return
      }
      const pendingQuestions = new Map(this.#value.pendingQuestions)
      pendingQuestions.set(rpcId, request)
      this.#patch({ pendingQuestions, composerState: 'composer.waitingForAnswer' })
      return
    }
    if (frameType === 'question/resolved') {
      const questionRpcId = recordString(payload, 'questionRpcId')
      if (questionRpcId === undefined) return
      this.#removeQuestion(questionRpcId, 'composer.agentContinuing')
      return
    }
    if (frameType !== 'session/event') return
    const sessionId = recordString(payload, 'sessionId')
    const eventValue = payload.event
    if (sessionId === undefined || !isSessionEvent(eventValue)) return
    const events = new Map(this.#value.events)
    const sessionEvents = compactCompletedChunks(mergeEvents(events.get(sessionId) ?? [], [eventValue]))
    events.set(sessionId, sessionEvents)
    const streams = new Map(this.#value.streams)
    setProjectedStream(streams, sessionId, sessionEvents)
    this.#patch({ events, streams })
    if (eventValue.type === 'turn/end') {
      this.#patch({ composerState: 'composer.factsSynced' })
      globalThis.setTimeout(() => void this.refreshSessions().catch(error => this.#fail(error)), 100)
    }
  }

  #receiveHost(payload: unknown): void {
    if (!isRecord(payload)) return
    const frameType = recordString(payload, 'type')
    if (frameType === 'host/session-added' || frameType === 'host/session-removed') {
      void this.refreshSessions().catch(error => this.#fail(error))
      return
    }
    if (frameType === 'host/session-status') {
      const sessionId = recordString(payload, 'sessionId')
      const running = recordBoolean(payload, 'running')
      if (sessionId === undefined || running === undefined) return
      const sessions = this.#value.sessions.map(item => item.sessionId === sessionId ? { ...item, running } : item)
      this.#patch({
        sessions,
        composerState: sessionId === this.#value.currentSessionId
          ? running ? 'composer.agentWorking' : 'composer.factsSynced'
          : this.#value.composerState,
      })
      return
    }
    if (frameType === 'host/agent-error') this.#fail(new Error(recordString(payload, 'message') ?? this.#translateText('error.agentFailed')))
  }

  #patch(patch: Partial<ConversationSnapshot>): void {
    this.#value = { ...this.#value, ...patch }
    for (const subscriber of this.#subscribers) subscriber()
  }

  #removeQuestion(rpcId: string, composerState: ConversationSnapshot['composerState']): void {
    if (!this.#value.pendingQuestions.has(rpcId)) return
    const pendingQuestions = new Map(this.#value.pendingQuestions)
    pendingQuestions.delete(rpcId)
    this.#patch({ pendingQuestions, composerState })
  }

  #fail(error: unknown, fatal = false): void {
    const message = error instanceof Error ? error.message : String(error)
    this.#patch({ error: message, ...(fatal ? { phase: 'failed' as const } : {}) })
    globalThis.clearTimeout(this.#toastTimer)
    this.#toastTimer = globalThis.setTimeout(() => this.dismissError(), 5000)
    console.error(error)
  }
}

function isSessionEvent(value: unknown): value is SessionEvent {
  return isRecord(value) && typeof value.type === 'string' && typeof value.seq === 'number' && typeof value.time === 'number'
}

function setProjectedStream(
  streams: Map<string, StreamDraft>,
  sessionId: string,
  events: readonly SessionEvent[],
): void {
  const draft = projectStream(events)
  if (draft === undefined) streams.delete(sessionId)
  else streams.set(sessionId, draft)
}

function historyState(
  events: readonly SessionEvent[],
  hasMore: boolean,
  fallbackBeforeSeq?: number,
): SessionHistoryState {
  return {
    beforeSeq: events[0]?.seq ?? fallbackBeforeSeq,
    hasMore,
    loading: false,
  }
}

function compactCompletedChunks(events: readonly SessionEvent[]): SessionEvent[] {
  let latestCompletedSeq = -1
  for (const event of events) {
    if (event.type === 'assistant/message' && event.seq > latestCompletedSeq) {
      latestCompletedSeq = event.seq
    }
  }
  if (latestCompletedSeq < 0) return [...events]
  return events.filter(event => event.type !== 'assistant/chunk' || event.seq > latestCompletedSeq)
}

function prependSession(
  sessions: readonly SessionSummary[],
  created: SessionSummary,
): SessionSummary[] {
  return [created, ...sessions.filter(summary => summary.sessionId !== created.sessionId)]
}

function appendSessions(
  sessions: readonly SessionSummary[],
  appended: readonly SessionSummary[],
): readonly SessionSummary[] {
  const known = new Set(sessions.map(summary => summary.sessionId))
  return [...sessions, ...appended.filter(summary => !known.has(summary.sessionId))]
}

function messageFromEvent(event: SessionEvent): MessageRow | undefined {
  if (event.type === 'user/message') return readMessage(event.data, event.seq, 'user', 'user')
  if (event.type === 'assistant/message' && isRecord(event.data)) return readMessage(event.data.message, event.seq, 'assistant')
  return undefined
}

function readMessage(
  value: unknown,
  seq: number,
  expectedRole: MessageRow['role'],
  expectedSourceKind?: string,
): MessageRow | undefined {
  if (!isRecord(value) || !Array.isArray(value.content)) return undefined
  if (recordString(value, 'role') !== expectedRole) return undefined
  if (expectedSourceKind !== undefined) {
    if (!isRecord(value.source) || recordString(value.source, 'kind') !== expectedSourceKind) return undefined
  }
  let text = ''
  let reasoning = ''
  for (const block of value.content) {
    if (!isRecord(block)) continue
    const blockType = recordString(block, 'type')
    if (blockType === 'text') text += recordString(block, 'text') ?? ''
    if (blockType === 'reasoning') reasoning += recordString(block, 'text') ?? ''
    if (blockType === 'tool-result' && Array.isArray(block.content)) text += flattenContent(block.content)
  }
  if (text === '' && reasoning === '') return undefined
  return {
    role: expectedRole,
    text,
    reasoning,
    streaming: false,
    seq,
  }
}

function readQuestionRequest(rpcId: string, payload: Record<string, unknown>): PendingQuestionRequest | undefined {
  const sessionId = recordString(payload, 'sessionId')
  if (sessionId === undefined || !Array.isArray(payload.questions) || payload.questions.length === 0) return undefined
  const questions: QuestionItem[] = []
  for (const rawQuestion of payload.questions) {
    const question = readQuestion(rawQuestion)
    if (question === undefined) return undefined
    questions.push(question)
  }
  return { rpcId, sessionId, questions }
}

function readQuestion(value: unknown): QuestionItem | undefined {
  if (!isRecord(value)) return undefined
  const id = recordString(value, 'id')
  const question = recordString(value, 'question')
  if (id === undefined || question === undefined) return undefined
  let options: QuestionOption[] | undefined
  if (value.options !== undefined) {
    if (!Array.isArray(value.options)) return undefined
    options = []
    for (const rawOption of value.options) {
      if (!isRecord(rawOption)) return undefined
      const label = recordString(rawOption, 'label')
      if (label === undefined) return undefined
      options.push({ label, description: recordString(rawOption, 'description') })
    }
  }
  const multiSelect = value.multiSelect
  if (multiSelect !== undefined && typeof multiSelect !== 'boolean') return undefined
  return {
    id,
    question,
    header: recordString(value, 'header'),
    detail: recordString(value, 'detail'),
    options,
    multiSelect,
  }
}

function flattenContent(content: readonly unknown[]): string {
  return content
    .filter(isRecord)
    .filter(block => recordString(block, 'type') === 'text')
    .map(block => recordString(block, 'text') ?? '')
    .join('')
}
