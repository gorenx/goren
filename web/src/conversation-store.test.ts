import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ConversationStore } from './conversation-store'
import type { Translator } from './i18n'
import type { BoundDefinition, BoundDefinitionDraft, BoundExtensionOption, BoundToolOption, SessionEvent, SessionSummary } from './types'
import { MockWebSocket } from './test/mock-websocket'

const translate: Translator = (messageKey, values) => {
  const suffix = values === undefined ? '' : ` ${JSON.stringify(values)}`
  return `${messageKey}${suffix}`
}

const session: SessionSummary = {
  sessionId: 'session-1',
  updatedAt: 10,
  running: false,
  blank: true,
}

describe('ConversationStore', () => {
  beforeEach(() => {
    MockWebSocket.reset()
    vi.stubGlobal('WebSocket', MockWebSocket)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('bootstraps one empty Host into a selected durable Session', async () => {
    const host = installMockHost()
    const store = new ConversationStore(translate)

    await store.start()

    expect(store.snapshot().phase).toBe('ready')
    expect(store.snapshot().currentSessionId).toBe(session.sessionId)
    expect(store.snapshot().sessions).toEqual([session])
    expect(MockWebSocket.instances.map(socket => socket.url).sort()).toEqual([
      'ws://localhost:3000/api/events.host',
      'ws://localhost:3000/api/events.mux',
    ])
    expect(host.methods).toEqual([
      'host.describe',
      'credentials.describe',
      'bound.list',
      'session.list',
      'session.create',
      'session.history',
      'session.list',
    ])

    store.dispose()
  })

  it('shows the new-conversation state immediately and admits only one create request', async () => {
    let releaseCreate: (() => void) | undefined
    const createWait = new Promise<void>(resolve => {
      releaseCreate = resolve
    })
    const host = installMockHost({ existingSession: true, createWait })
    const store = new ConversationStore(translate)
    await store.start()

    const firstCreate = store.createSession()
    const duplicateCreate = store.createSession()

    expect(store.snapshot().creatingSession).toBe(true)
    expect(store.snapshot().currentSessionId).toBeUndefined()
    expect(store.snapshot().composerState).toBe('composer.creatingSession')
    expect(host.methods.filter(method => method === 'session.create')).toHaveLength(1)

    releaseCreate?.()
    await Promise.all([firstCreate, duplicateCreate])

    expect(store.snapshot().creatingSession).toBe(false)
    expect(store.snapshot().currentSessionId).toBe('session-2')
    expect(store.snapshot().sessions[0]?.sessionId).toBe('session-2')
    store.dispose()
  })

  it('loads the Session list progressively from the returned cursor', async () => {
    const older: SessionSummary = {
      sessionId: 'session-older',
      updatedAt: 5,
      running: false,
      blank: false,
    }
    const host = installMockHost({
      sessionPages: {
        first: {
          items: [session],
          nextCursor: 'older-page',
        },
        'older-page': {
          items: [older],
        },
      },
    })
    const store = new ConversationStore(translate)

    await store.start()
    expect(store.snapshot().sessions).toEqual([session])
    expect(store.snapshot().nextSessionCursor).toBe('older-page')

    await store.loadMoreSessions()

    expect(store.snapshot().sessions).toEqual([session, older])
    expect(store.snapshot().nextSessionCursor).toBeUndefined()
    expect(host.calls.filter(call => call.method === 'session.list').map(call => call.payload)).toEqual([
      { limit: 50 },
      { cursor: 'older-page', limit: 50 },
    ])
    store.dispose()
  })

  it('selects the created session before list and history synchronization finish', async () => {
    let releaseSynchronization: (() => void) | undefined
    const synchronizationWait = new Promise<void>(resolve => {
      releaseSynchronization = resolve
    })
    const host = installMockHost({
      historyWait: synchronizationWait,
      listWait: synchronizationWait,
    })
    const store = new ConversationStore(translate)

    const creation = store.createSession()
    await waitForMethods(host.methods, ['session.create', 'session.history', 'session.list'])

    expect(store.snapshot().currentSessionId).toBe(session.sessionId)
    expect(store.snapshot().sessions).toEqual([expect.objectContaining({ sessionId: session.sessionId })])
    expect(store.snapshot().composerState).toBe('composer.ready')
    expect(store.snapshot().creatingSession).toBe(false)

    releaseSynchronization?.()
    await creation
    store.dispose()
  })

  it('restores the previous selection when Session creation fails', async () => {
    installMockHost({ existingSession: true, createFailure: 'create rejected' })
    const store = new ConversationStore(translate)
    await store.start()

    await store.createSession()

    expect(store.snapshot().creatingSession).toBe(false)
    expect(store.snapshot().currentSessionId).toBe(session.sessionId)
    expect(store.snapshot().composerState).toBe('composer.ready')
    expect(store.snapshot().error).toBe('create rejected')
    store.dispose()
  })

  it('does not apply a replayed assistant chunk twice', async () => {
    installMockHost({ existingSession: true })
    const store = new ConversationStore(translate)
    await store.start()
    const mux = MockWebSocket.instances.find(socket => socket.url.endsWith('/api/events.mux'))
    if (mux === undefined) throw new Error('web test: mux downlink is missing')
    const frame = {
      type: 'server-request',
      rpcId: 'event-1',
      payload: {
        type: 'session/event',
        sessionId: session.sessionId,
        event: {
          type: 'assistant/chunk',
          seq: 3,
          time: 20,
          data: {
            chunk: {
              type: 'text-delta',
              text: 'hello',
            },
          },
        },
      },
    }

    mux.receive(frame)
    mux.receive(frame)

    expect(store.currentMessages()).toEqual([
      {
        role: 'assistant',
        text: 'hello',
        reasoning: '',
        streaming: true,
        interrupted: false,
        seq: 3,
      },
    ])
    store.dispose()
  })

  it('reconstructs an interrupted draft from Session history', async () => {
    installMockHost({
      existingSession: true,
      historyEvents: [
        {
          type: 'assistant/chunk',
          seq: 4,
          time: 20,
          data: {
            chunk: {
              type: 'text-delta',
              text: 'partial answer',
            },
          },
        },
        {
          type: 'turn/end',
          seq: 5,
          time: 21,
          data: {
            reason: {
              kind: 'interrupted',
            },
          },
        },
      ],
    })
    const store = new ConversationStore(translate)

    await store.start()

    expect(store.currentMessages()).toEqual([
      {
        role: 'assistant',
        text: 'partial answer',
        reasoning: '',
        streaming: false,
        interrupted: true,
        seq: 5,
      },
    ])
    store.dispose()
  })

  it('drops completed chunks while keeping the unfinished tail', async () => {
    installMockHost({
      existingSession: true,
      historyEvents: [
        {
          type: 'assistant/chunk',
          seq: 1,
          time: 10,
          data: { chunk: { type: 'text-delta', text: 'completed draft' } },
        },
        {
          type: 'assistant/message',
          seq: 2,
          time: 11,
          data: {
            message: {
              role: 'assistant',
              content: [{ type: 'text', text: 'completed answer' }],
            },
          },
        },
        {
          type: 'assistant/chunk',
          seq: 3,
          time: 12,
          data: { chunk: { type: 'text-delta', text: 'unfinished answer' } },
        },
      ],
    })
    const store = new ConversationStore(translate)

    await store.start()

    expect(store.currentEvents().map(event => event.seq)).toEqual([2, 3])
    expect(store.currentMessages().map(message => message.text)).toEqual([
      'completed answer',
      'unfinished answer',
    ])
    store.dispose()
  })

  it('loads older history from the raw page cursor', async () => {
    const host = installMockHost({
      existingSession: true,
      historyPages: {
        tail: {
          events: [
            { type: 'assistant/chunk', seq: 20, time: 20 },
            { type: 'assistant/message', seq: 21, time: 21 },
          ],
          hasMore: true,
        },
        '20': {
          events: [{ type: 'user/message', seq: 5, time: 5 }],
          hasMore: false,
        },
      },
    })
    const store = new ConversationStore(translate)
    await store.start()

    expect(store.currentHistory()).toEqual({ beforeSeq: 20, hasMore: true, loading: false })

    await store.loadOlderHistory()

    const historyCalls = host.calls.filter(call => call.method === 'session.history')
    expect(historyCalls.map(call => call.payload)).toEqual([
      { sessionId: session.sessionId, maxMessages: 20 },
      { sessionId: session.sessionId, beforeSeq: 20, maxMessages: 20 },
    ])
    expect(store.currentHistory()).toEqual({ beforeSeq: 5, hasMore: false, loading: false })
    expect(store.currentEvents().map(event => event.seq)).toEqual([5, 21])
    store.dispose()
  })

  it('keeps a live event that arrives while history is loading', async () => {
    let releaseHistory: (() => void) | undefined
    const historyWait = new Promise<void>(resolve => {
      releaseHistory = resolve
    })
    installMockHost({
      existingSession: true,
      historyWait,
    })
    const store = new ConversationStore(translate)
    const starting = store.start()
    await waitForMux()
    const mux = MockWebSocket.instances.find(socket => socket.url.endsWith('/api/events.mux'))
    if (mux === undefined) throw new Error('web test: mux downlink is missing')

    mux.receive({
      type: 'server-request',
      rpcId: 'event-during-history',
      payload: {
        type: 'session/event',
        sessionId: session.sessionId,
        event: {
          type: 'assistant/chunk',
          seq: 8,
          time: 30,
          data: {
            chunk: {
              type: 'text-delta',
              text: 'live answer',
            },
          },
        },
      },
    })
    releaseHistory?.()
    await starting

    expect(store.currentMessages()).toEqual([
      {
        role: 'assistant',
        text: 'live answer',
        reasoning: '',
        streaming: true,
        interrupted: false,
        seq: 8,
      },
    ])
    store.dispose()
  })

  it('correlates a structured question answer with the Host request', async () => {
    const host = installMockHost({ existingSession: true })
    const store = new ConversationStore(translate)
    await store.start()
    const mux = MockWebSocket.instances.find(socket => socket.url.endsWith('/api/events.mux'))
    if (mux === undefined) throw new Error('web test: mux downlink is missing')
    mux.receive({
      type: 'server-request',
      rpcId: 'question-1',
      payload: {
        type: 'question/requested',
        sessionId: session.sessionId,
        questions: [
          {
            id: 'mode',
            question: 'Choose a mode',
            options: [{ label: 'Careful' }],
          },
        ],
      },
    })
    const pending = store.currentQuestion()
    if (pending === undefined) throw new Error('web test: pending question is missing')

    await store.answerQuestion(pending, [{ id: 'mode', selected: ['Careful'] }])

    expect(store.currentQuestion()).toBeUndefined()
    expect(host.responses).toEqual([
      {
        type: 'client-response',
        rpcId: 'question-1',
        result: {
          ok: true,
          value: {
            sessionId: session.sessionId,
            answer: {
              answers: [{ id: 'mode', selected: ['Careful'] }],
            },
          },
        },
      },
    ])
    store.dispose()
  })

  it('creates and replaces complete Bound Definitions', async () => {
    const host = installMockHost({ existingSession: true })
    const store = new ConversationStore(translate)
    await store.start()
    await store.refreshBoundTools()
    const draft: BoundDefinitionDraft = {
      name: 'researcher',
      enabled: true,
      systemPrompt: 'Research in the background.',
      toolRestriction: { allow: [] },
      extensions: ['report'],
    }

    const created = await store.createBoundDefinition(draft)
    expect(created.revision).toBe(1)
    expect(store.snapshot().boundDefinitions).toEqual([created])

    const replaced = await store.replaceBoundDefinition(1, {
      ...draft,
      enabled: false,
      systemPrompt: 'Revised prompt.',
    })
    expect(replaced).toEqual({
      ...draft,
      enabled: false,
      systemPrompt: 'Revised prompt.',
      revision: 2,
    })
    expect(store.snapshot().boundDefinitions).toEqual([replaced])
    expect(host.calls.filter(call => call.method.startsWith('bound.')).map(call => call.method)).toEqual([
      'bound.list',
      'bound.tools',
      'bound.create',
      'bound.list',
      'bound.replace',
      'bound.list',
    ])
    store.dispose()
  })

  it('keeps Bound catalog failures local and permits retry', async () => {
    const options: MockHostOptions = {
      existingSession: true,
      boundToolsFailure: 'directory unavailable',
      boundExtensionsFailure: 'extension directory unavailable',
    }
    const host = installMockHost(options)
    const store = new ConversationStore(translate)
    await store.start()

    expect(store.snapshot().phase).toBe('ready')
    expect(host.methods).not.toContain('bound.tools')

    await store.refreshBoundTools()
    expect(store.snapshot().boundToolsState).toBe('failed')
    expect(store.snapshot().boundToolsError).toBe('directory unavailable')
    expect(store.snapshot().error).toBeUndefined()

    await store.refreshBoundExtensions()
    expect(store.snapshot().boundExtensionsState).toBe('failed')
    expect(store.snapshot().boundExtensionsError).toBe('extension directory unavailable')
    expect(store.snapshot().error).toBeUndefined()

    options.boundToolsFailure = undefined
    options.boundExtensionsFailure = undefined
    await store.refreshBoundTools()
    await store.refreshBoundExtensions()
    expect(store.snapshot().boundToolsState).toBe('ready')
    expect(store.snapshot().boundTools).toEqual([
      {
        name: 'delegate',
        description: 'Start a child agent.',
      },
    ])
    expect(store.snapshot().boundExtensionsState).toBe('ready')
    expect(store.snapshot().boundExtensions).toEqual([
      {
        name: 'memory',
      },
    ])
    expect(host.methods.filter(method => method === 'bound.tools')).toHaveLength(2)
    expect(host.methods.filter(method => method === 'bound.extensions')).toHaveLength(2)
    store.dispose()
  })
})

interface MockHostOptions {
  existingSession?: boolean
  createFailure?: string
  createWait?: Promise<void>
  historyEvents?: SessionEvent[]
  historyHasMore?: boolean
  historyPages?: Record<string, { events: SessionEvent[], hasMore: boolean }>
  historyWait?: Promise<void>
  listWait?: Promise<void>
  sessionPages?: Record<string, { items: SessionSummary[], nextCursor?: string }>
  boundDefinitions?: BoundDefinition[]
  boundTools?: BoundToolOption[]
  boundToolsFailure?: string
  boundExtensions?: BoundExtensionOption[]
  boundExtensionsFailure?: string
}

function installMockHost(options: MockHostOptions = {}): {
  methods: string[]
  responses: unknown[]
  calls: Array<{ method: string, payload: unknown }>
} {
  let sessions = options.existingSession === true ? [session] : []
  let boundDefinitions = [...(options.boundDefinitions ?? [])]
  const methods: string[] = []
  const responses: unknown[] = []
  const calls: Array<{ method: string, payload: unknown }> = []
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : new URL(input.url).pathname
    if (!path.startsWith('/api/')) return response({}, 404)
    const request: unknown = JSON.parse(String(init?.body))
    if (path === '/api/respond') {
      responses.push(request)
      return response({ accepted: true })
    }
    const call = request as {
      rpcId: string
      method: string
      payload: unknown
    }
    methods.push(call.method)
    calls.push({ method: call.method, payload: call.payload })
    let value: unknown
    switch (call.method) {
    case 'host.describe':
      value = {
        version: 'test',
        cwd: '/workspace',
        model: 'mock',
        attachedSessions: 0,
        canOpenPath: false,
      }
      break
    case 'credentials.describe':
      value = {
        credentials: {
          DEEPSEEK_API_KEY: {
            configured: true,
            source: 'environment',
            writable: false,
          },
        },
      }
      break
    case 'bound.list':
      value = { definitions: boundDefinitions }
      break
    case 'bound.tools':
      if (options.boundToolsFailure !== undefined) {
        return response({
          rpcId: call.rpcId,
          result: {
            ok: false,
            error: {
              message: options.boundToolsFailure,
            },
          },
        })
      }
      value = {
        tools: options.boundTools ?? [
          {
            name: 'delegate',
            description: 'Start a child agent.',
          },
        ],
      }
      break
    case 'bound.extensions':
      if (options.boundExtensionsFailure !== undefined) {
        return response({
          rpcId: call.rpcId,
          result: {
            ok: false,
            error: {
              message: options.boundExtensionsFailure,
            },
          },
        })
      }
      value = {
        extensions: options.boundExtensions ?? [
          {
            name: 'memory',
          },
        ],
      }
      break
    case 'bound.create': {
      const payload = call.payload as { definition: BoundDefinitionDraft }
      const committed: BoundDefinition = {
        ...payload.definition,
        revision: 1,
      }
      boundDefinitions = [...boundDefinitions, committed]
      value = { definition: committed }
      break
    }
    case 'bound.replace': {
      const payload = call.payload as {
        expectedRevision: number
        definition: BoundDefinitionDraft
      }
      const committed: BoundDefinition = {
        ...payload.definition,
        revision: payload.expectedRevision + 1,
      }
      boundDefinitions = boundDefinitions.map(current =>
        current.name === committed.name ? committed : current)
      value = { definition: committed }
      break
    }
    case 'session.list':
      await options.listWait
      const listPayload = call.payload as { cursor?: string }
      value = options.sessionPages?.[listPayload.cursor ?? 'first'] ?? { items: sessions }
      break
    case 'session.create':
      await options.createWait
      if (options.createFailure !== undefined) {
        return response({
          rpcId: call.rpcId,
          result: {
            ok: false,
            error: {
              message: options.createFailure,
            },
          },
        })
      }
      const createdSession = sessions.length === 0
        ? session
        : {
            ...session,
            sessionId: 'session-2',
            updatedAt: 20,
          }
      sessions = [createdSession, ...sessions]
      value = {
        sessionId: createdSession.sessionId,
      }
      break
    case 'session.history':
      await options.historyWait
      const historyPayload = call.payload as { beforeSeq?: number }
      const pageKey = historyPayload.beforeSeq === undefined ? 'tail' : String(historyPayload.beforeSeq)
      const configuredPage = options.historyPages?.[pageKey]
      value = {
        events: (configuredPage?.events ?? options.historyEvents ?? []).map(event => ({ event })),
        hasMore: configuredPage?.hasMore ?? options.historyHasMore ?? false,
      }
      break
    default:
      return response({}, 404)
    }
    return response({
      rpcId: call.rpcId,
      result: {
        ok: true,
        value,
      },
    })
  }))
  return { methods, responses, calls }
}

async function waitForMux(): Promise<void> {
  for (let attempt = 0; attempt < 50; attempt++) {
    if (MockWebSocket.instances.some(socket => socket.url.endsWith('/api/events.mux'))) return
    await new Promise(resolve => globalThis.setTimeout(resolve, 0))
  }
  throw new Error('web test: mux downlink was not opened')
}

async function waitForMethods(methods: readonly string[], expected: readonly string[]): Promise<void> {
  for (let attempt = 0; attempt < 20; attempt++) {
    if (expected.every(method => methods.includes(method))) return
    await Promise.resolve()
  }
  throw new Error(`web test: methods did not arrive: ${expected.join(', ')}`)
}

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: {
      'content-type': 'application/json',
    },
  })
}
