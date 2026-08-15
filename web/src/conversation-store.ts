import { HarnessAPI } from './api'
import type {
  ConversationSnapshot,
  HostDescription,
  MessageRow,
  SessionCreateValue,
  SessionEvent,
  SessionHistoryValue,
  SessionListValue,
  SessionSummary,
  StreamDraft,
} from './types'
import { isRecord, recordBoolean, recordString } from './types'

type Subscriber = () => void

const initialSnapshot: ConversationSnapshot = {
  phase: 'booting',
  sessions: [],
  events: new Map(),
  streams: new Map(),
  localTitles: new Map(),
  onlineDownlinks: 0,
  composerState: '正在连接 Go Agent',
}

export class ConversationStore {
  readonly #subscribers = new Set<Subscriber>()
  readonly #api: HarnessAPI
  #value = initialSnapshot
  #selectionVersion = 0
  #toastTimer?: number
  #started = false

  constructor() {
    this.#api = new HarnessAPI(
      payload => this.#receiveMux(payload),
      payload => this.#receiveHost(payload),
      count => this.#patch({ onlineDownlinks: count }),
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
      this.#patch({ host, composerState: '正在同步会话' })
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
    const result = await this.#api.call<SessionListValue>('session.list', {})
    const sessions = [...(result.items ?? [])]
      .filter(item => item.origin !== 'subagent')
      .sort((left, right) => right.updatedAt - left.updatedAt)
    this.#patch({ sessions })
  }

  async createSession(): Promise<void> {
    try {
      const payload = this.#value.host?.cwd ? { cwd: this.#value.host.cwd } : {}
      const created = await this.#api.call<SessionCreateValue>('session.create', payload)
      await this.refreshSessions()
      await this.selectSession(created.sessionId)
    } catch (error) {
      this.#fail(error)
    }
  }

  async selectSession(sessionId: string): Promise<void> {
    const selectionVersion = ++this.#selectionVersion
    const streams = new Map(this.#value.streams)
    streams.delete(sessionId)
    this.#patch({ currentSessionId: sessionId, streams, composerState: '正在读取历史' })
    try {
      const history = await this.#api.call<SessionHistoryValue>('session.history', { sessionId, maxMessages: 100 })
      if (selectionVersion !== this.#selectionVersion) return
      const events = new Map(this.#value.events)
      events.set(sessionId, (history.events ?? []).map(entry => entry.event))
      this.#patch({ events, composerState: this.currentSession()?.running ? 'Agent 正在处理' : '准备就绪' })
    } catch (error) {
      if (selectionVersion === this.#selectionVersion) this.#fail(error)
    }
  }

  async submitPrompt(rawText: string): Promise<void> {
    const text = rawText.trim()
    const sessionId = this.#value.currentSessionId
    if (text === '' || sessionId === undefined) return
    const localTitles = new Map(this.#value.localTitles)
    localTitles.set(sessionId, text.slice(0, 52))
    this.#patch({ localTitles, composerState: '已进入 Agent 队列' })
    try {
      await this.#api.call<unknown>('session.prompt', {
        sessionId,
        mode: 'queue',
        content: [{ type: 'text', text }],
        clientTimeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
      })
    } catch (error) {
      this.#patch({ composerState: '发送失败' })
      this.#fail(error)
      throw error
    }
  }

  currentSession(): SessionSummary | undefined {
    return this.#value.sessions.find(item => item.sessionId === this.#value.currentSessionId)
  }

  currentEvents(): readonly SessionEvent[] {
    const sessionId = this.#value.currentSessionId
    return sessionId === undefined ? [] : this.#value.events.get(sessionId) ?? []
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
      rows.push({ role: 'assistant', ...draft, streaming: true })
    }
    return rows
  }

  sessionTitle(summary: SessionSummary): string {
    const local = this.#value.localTitles.get(summary.sessionId)
    const projected = summary.projections?.values.title
    if (local !== undefined) return local
    if (typeof projected === 'string' && projected.trim() !== '') return projected
    return summary.blank ? '新对话' : `对话 ${summary.sessionId.slice(0, 8)}`
  }

  relativeTime(timestamp: number): string {
    const elapsed = Math.max(0, Date.now() - timestamp)
    if (elapsed < 60_000) return '刚刚'
    if (elapsed < 3_600_000) return `${String(Math.floor(elapsed / 60_000))} 分钟前`
    if (elapsed < 86_400_000) return `${String(Math.floor(elapsed / 3_600_000))} 小时前`
    return new Date(timestamp).toLocaleDateString('zh-CN', { month: 'short', day: 'numeric' })
  }

  dismissError(): void {
    this.#patch({ error: undefined })
  }

  #receiveMux(payload: unknown): void {
    if (!isRecord(payload)) return
    const frameType = recordString(payload, 'type')
    if (frameType === 'stream/error') {
      const error = isRecord(payload.error) ? recordString(payload.error, 'message') : undefined
      this.#fail(new Error(error ?? '事件流已断开'))
      return
    }
    if (frameType !== 'session/event') return
    const sessionId = recordString(payload, 'sessionId')
    const eventValue = payload.event
    if (sessionId === undefined || !isSessionEvent(eventValue)) return
    const events = new Map(this.#value.events)
    const sessionEvents = [...(events.get(sessionId) ?? [])]
    if (!sessionEvents.some(event => event.seq === eventValue.seq)) {
      sessionEvents.push(eventValue)
      sessionEvents.sort((left, right) => left.seq - right.seq)
      events.set(sessionId, sessionEvents)
    }
    const streams = new Map(this.#value.streams)
    applyStreamEvent(streams, sessionId, eventValue)
    this.#patch({ events, streams })
    if (eventValue.type === 'turn/end') {
      this.#patch({ composerState: '事实已同步，准备就绪' })
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
          ? running ? 'Agent 正在处理' : '事实已同步，准备就绪'
          : this.#value.composerState,
      })
      return
    }
    if (frameType === 'host/agent-error') this.#fail(new Error(recordString(payload, 'message') ?? 'Agent 执行失败'))
  }

  #patch(patch: Partial<ConversationSnapshot>): void {
    this.#value = { ...this.#value, ...patch }
    for (const subscriber of this.#subscribers) subscriber()
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

function applyStreamEvent(
  streams: Map<string, StreamDraft>,
  sessionId: string,
  event: SessionEvent,
): void {
  if (event.type === 'assistant/chunk' && isRecord(event.data) && isRecord(event.data.chunk)) {
    const chunk = event.data.chunk
    const chunkType = recordString(chunk, 'type')
    const text = recordString(chunk, 'text') ?? ''
    const draft = { ...(streams.get(sessionId) ?? { text: '', reasoning: '' }) }
    if (chunkType === 'text-delta') draft.text += text
    if (chunkType === 'reasoning-delta') draft.reasoning += text
    streams.set(sessionId, draft)
  }
  if (event.type === 'assistant/message' || event.type === 'turn/end') streams.delete(sessionId)
}

function messageFromEvent(event: SessionEvent): MessageRow | undefined {
  if (event.type === 'user/message') return readMessage(event.data, event.seq)
  if (event.type === 'assistant/message' && isRecord(event.data)) return readMessage(event.data.message, event.seq)
  if (event.type === 'tool/result' && isRecord(event.data)) return readMessage(event.data.message, event.seq)
  return undefined
}

function readMessage(value: unknown, seq: number): MessageRow | undefined {
  if (!isRecord(value) || !Array.isArray(value.content)) return undefined
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
    role: recordString(value, 'role') === 'assistant' ? 'assistant' : 'user',
    text,
    reasoning,
    streaming: false,
    seq,
  }
}

function flattenContent(content: readonly unknown[]): string {
  return content
    .filter(isRecord)
    .filter(block => recordString(block, 'type') === 'text')
    .map(block => recordString(block, 'text') ?? '')
    .join('')
}
