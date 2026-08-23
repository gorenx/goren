import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ConversationStore } from './conversation-store'
import type { Translator } from './i18n'
import type { SessionEvent, SessionSummary } from './types'
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
      'session.list',
      'session.create',
      'session.list',
      'session.history',
    ])

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
})

interface MockHostOptions {
  existingSession?: boolean
  historyEvents?: SessionEvent[]
  historyWait?: Promise<void>
}

function installMockHost(options: MockHostOptions = {}): { methods: string[], responses: unknown[] } {
  let created = options.existingSession === true
  const methods: string[] = []
  const responses: unknown[] = []
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
    case 'session.list':
      value = {
        items: created ? [session] : [],
      }
      break
    case 'session.create':
      created = true
      value = {
        sessionId: session.sessionId,
      }
      break
    case 'session.history':
      await options.historyWait
      value = {
        events: (options.historyEvents ?? []).map(event => ({ event })),
        hasMore: false,
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
  return { methods, responses }
}

async function waitForMux(): Promise<void> {
  for (let attempt = 0; attempt < 20; attempt++) {
    if (MockWebSocket.instances.some(socket => socket.url.endsWith('/api/events.mux'))) return
    await Promise.resolve()
  }
  throw new Error('web test: mux downlink was not opened')
}

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: {
      'content-type': 'application/json',
    },
  })
}
