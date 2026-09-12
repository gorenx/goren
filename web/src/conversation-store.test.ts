import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ConversationStore } from './conversation-store'
import type { Translator } from './i18n'
import type { SessionEvent, SessionSummary, WorkspaceListValue } from './types'
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

  it('retains child Sessions and reads their history and live output independently', async () => {
    const child = { ...session, sessionId: 'child', parentSessionId: session.sessionId, origin: 'subagent' }
    const host = installMockHost({ existingSession: true, sessionPages: { first: { items: [child, session] } } })
    const store = new ConversationStore(translate)
    await store.start()
    expect(store.snapshot().currentSessionId).toBe(session.sessionId)
    expect(store.snapshot().sessions).toContainEqual(child)
    await store.selectSession('child')
    expect(host.calls).toContainEqual({ method: 'session.history', payload: { sessionId: 'child', maxMessages: 20 } })
    const mux = MockWebSocket.instances.find(socket => socket.url.endsWith('/api/events.mux'))!
    mux.open()
    mux.receive({ type: 'server-request', rpcId: 'child-output', payload: {
      type: 'session/event', sessionId: 'child', event: {
        type: 'assistant/chunk', seq: 3, time: 20,
        data: { chunk: { type: 'text-delta', text: 'child output' } },
      },
    } })
    expect(store.currentMessages().some(message => message.text === 'child output')).toBe(true)
    await store.selectSession(session.sessionId)
    expect(store.currentMessages().some(message => message.text === 'child output')).toBe(false)
    store.dispose()
  })

  it.each(['/b', null])('uses the native folder picker result %s without submitting a prompt', async pickedPath => {
    const host = installMockHost({ existingSession: true, workspaces: workspaceListing(), pickedPath })
    const store = new ConversationStore(translate)
    await store.start()
    await store.pickWorkspace(new AbortController().signal)
    expect(host.calls).toContainEqual({ method: 'directoryPicker.pick', payload: {} })
    expect(host.methods).not.toContain('session.prompt')
    if (pickedPath === null) {
      expect(host.methods).not.toContain('workspace.create')
      expect(host.methods).not.toContain('session.create')
      expect(store.snapshot().currentSessionId).toBe(session.sessionId)
    } else {
      expect(host.calls).toContainEqual({ method: 'workspace.create', payload: { path: '/b' } })
      expect(store.currentSession()?.cwd).toBe('/b')
    }
    store.dispose()
  })

  it('finds a reusable workspace Session beyond the first page and keeps its directory after sidebar refresh', async () => {
    const blank = { ...session, sessionId: 'blank-b', cwd: '/b' }
    const rejected = [
      { ...blank, sessionId: 'archived' },
      { ...blank, sessionId: 'used', blank: false },
      { ...blank, sessionId: 'running', running: true },
      { ...blank, sessionId: 'child', origin: 'subagent' },
      { ...blank, sessionId: 'mismatch', cwd: '/a' },
    ]
    const host = installMockHost({
      existingSession: true,
      workspaces: { items: [{ workspaceId: 'b', path: '/b', title: 'B', sessionIds: [...rejected.map(item => item.sessionId), blank.sessionId] }], archivedSessionIds: ['archived'] },
      sessionPages: {
        first: { items: [session, ...rejected, { ...blank, sessionId: 'unaccounted' }], nextCursor: 'older' },
        older: { items: [blank] },
      },
    })
    const store = new ConversationStore(translate)
    await store.start()
    await store.openWorkspace({ workspaceId: 'b' })
    expect(store.snapshot().currentSessionId).toBe('blank-b')
    expect(host.methods).not.toContain('session.create')
    expect(host.calls).toContainEqual({ method: 'session.list', payload: { limit: 50, cursor: 'older' } })
    expect(host.calls).toContainEqual({ method: 'session.history', payload: { sessionId: 'blank-b', maxMessages: 20 } })
    await store.refreshSessions()
    expect(store.currentSession()?.cwd).toBe('/b')
    const reads = host.methods.filter(method => method === 'session.list').length
    await store.openWorkspace({ workspaceId: 'b' })
    expect(host.methods.filter(method => method === 'session.list')).toHaveLength(reads)
    store.dispose()
  })

  it('registers a server path and creates with the canonical Workspace identity, then inherits it for new conversations', async () => {
    const host = installMockHost({ existingSession: true, workspaces: workspaceListing() })
    const store = new ConversationStore(translate)
    await store.start()
    await store.openWorkspace({ path: '/symlink-b' })
    expect(host.calls).toContainEqual({ method: 'workspace.create', payload: { path: '/symlink-b' } })
    expect(host.calls).toContainEqual({ method: 'session.create', payload: { workspaceId: 'b' } })
    expect(store.currentSession()?.cwd).toBe('/b')
    expect(store.snapshot().sessions.find(item => item.sessionId === session.sessionId)).toEqual(session)
    await store.createSession()
    expect(host.calls.filter(call => call.method === 'session.create')).toEqual([
      { method: 'session.create', payload: { workspaceId: 'b' } },
      { method: 'session.create', payload: { workspaceId: 'b' } },
    ])
    store.dispose()
  })

  it.each(['path', 'creation'])('keeps the original Session when workspace %s fails', async failure => {
    installMockHost({ existingSession: true, workspaces: workspaceListing(), ...(failure === 'path' ? { workspaceFailure: 'invalid directory' } : { createFailure: 'creation failed' }) })
    const store = new ConversationStore(translate)
    await store.start()
    await expect(store.openWorkspace({ path: '/b' })).rejects.toThrow()
    expect(store.snapshot().currentSessionId).toBe(session.sessionId)
    expect(store.snapshot().creatingSession).toBe(false)
    store.dispose()
  })

  it.each(['cancel', 'navigate'])('does not redirect after %s during directory creation and ignores duplicate clicks', async action => {
    let release: (() => void) | undefined
    const wait = new Promise<void>(resolve => { release = resolve })
    const host = installMockHost({ existingSession: true, workspaces: workspaceListing(), createWait: wait })
    const store = new ConversationStore(translate)
    await store.start()
    const controller = new AbortController()
    const pending = store.openWorkspace({ workspaceId: 'b' }, controller.signal)
    await vi.waitFor(() => expect(host.methods).toContain('session.create'))
    await store.openWorkspace({ workspaceId: 'b' })
    if (action === 'cancel') controller.abort()
    else await store.selectSession(session.sessionId)
    release?.()
    await pending.catch(() => { /* Transport cancellation is also allowed. */ })
    expect(host.methods.filter(method => method === 'session.create')).toHaveLength(1)
    expect(store.snapshot().currentSessionId).toBe(session.sessionId)
    expect(store.snapshot().creatingSession).toBe(false)
    store.dispose()
  })

  it.each(['CUSTOM_DEEPSEEK_KEY', undefined])('uses the saved credential reference for reading and mutations: %s', async reference => {
    const host = installMockHost({ credentialRef: reference, pendingRestart: true })
    const store = new ConversationStore(translate)
    await store.refreshCredential()
    const expected = reference ?? 'DEEPSEEK_API_KEY'
    expect(store.snapshot().credentialRef).toBe(expected)
    expect(store.snapshot().credentialPendingRestart).toBe(true)
    expect(host.calls).toContainEqual({ method: 'credentials.describe', payload: { refs: [expected] } })
    await store.saveCredential('fixture-key')
    await store.unsetCredential()
    expect(host.calls).toContainEqual({ method: 'credentials.set', payload: { ref: expected, value: 'fixture-key' } })
    expect(host.calls).toContainEqual({ method: 'credentials.unset', payload: { ref: expected } })
    store.dispose()
  })

  it.each([{ settingsFailure: 'unavailable' }, { missingProvider: true }])('does not write a fallback credential when Settings cannot identify it: %j', async options => {
    const host = installMockHost(options)
    const store = new ConversationStore(translate)
    await expect(store.refreshCredential()).rejects.toThrow()
    await expect(store.saveCredential('fixture-key')).rejects.toThrow()
    await expect(store.unsetCredential()).rejects.toThrow()
    expect(store.snapshot().credentialLoaded).toBe(false)
    expect(host.calls.every(call => !call.method.startsWith('credentials.'))).toBe(true)
    store.dispose()
  })

  it('ignores stale credential metadata after a newer Settings refresh', async () => {
    let release: (() => void) | undefined
    const pending = new Promise<void>(resolve => { release = resolve })
    let reads = 0
    vi.stubGlobal('fetch', vi.fn(async (_input: unknown, init?: RequestInit) => {
      const envelope = JSON.parse(String(init?.body))
      let value: unknown
      if (envelope.method === 'settings.describe') {
        const reference = ++reads === 1 ? 'OLD_KEY' : 'NEW_KEY'
        value = { writable: true, namespaces: [{ ns: 'llmprovider', name: 'deepseek', value: { apiKeyEnv: reference }, revision: reads, applies: 'restart' }] }
      } else {
        const reference = envelope.payload.refs[0]
        if (reference === 'OLD_KEY') await pending
        value = { credentials: { [reference]: { configured: true, writable: true } } }
      }
      return response({ rpcId: envelope.rpcId, result: { ok: true, value } })
    }))
    const store = new ConversationStore(translate)
    const earlier = store.refreshCredential()
    await vi.waitFor(() => expect(reads).toBe(1))
    await store.refreshCredential()
    release?.()
    await earlier
    expect(store.snapshot().credentialRef).toBe('NEW_KEY')
    store.dispose()
  })

  it('refuses images on a text model without automatically selecting another model', async () => {
    const host = installMockHost({ existingSession: true })
    const store = new ConversationStore(translate)
    await store.start()
    const baseFetch = globalThis.fetch
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const envelope = JSON.parse(String(init?.body))
      if (envelope.method === 'session.models') return new Response(JSON.stringify({ rpcId: envelope.rpcId, result: { ok: true, value: { current: { provider: 'deepseek-official', model: 'deepseek-v4-flash' }, groups: [] } } }))
      return baseFetch(input, init)
    }))
    await expect(store.submitPrompt('', [{ type: 'image', mediaType: 'image/png', data: 'AQ==', name: 'picture' }])).rejects.toThrow('composer.switchVision')
    expect(host.calls.some(entry => entry.method === 'session.selectModel' || entry.method === 'session.prompt')).toBe(false)
    store.dispose()
  })

  it('keeps execution running after cancel acknowledgement until the Host reports idle', async () => {
    const host = installMockHost({ existingSession: true })
    const store = new ConversationStore(translate)
    await store.start()
    const socket = MockWebSocket.instances.find(item => item.url.endsWith('/api/events.host'))!
    socket.receive({ type: 'server-request', rpcId: 'running', payload: {
      type: 'host/session-status', sessionId: session.sessionId, running: true,
    } })
    expect(store.currentSession()?.running).toBe(true)
    await store.cancelCurrentTurn()
    expect(host.calls).toContainEqual(expect.objectContaining({ method: 'session.cancel', payload: { sessionId: session.sessionId } }))
    expect(store.currentSession()?.running).toBe(true)
    socket.receive({ type: 'server-request', rpcId: 'idle', payload: {
      type: 'host/session-status', sessionId: session.sessionId, running: false,
    } })
    expect(store.currentSession()?.running).toBe(false)
    store.dispose()
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
    expect(host.methods.filter(method => method !== 'credentials.describe')).toEqual([
      'host.describe',
      'settings.describe',
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
    await vi.waitFor(() => expect(host.methods.filter(method => method === 'session.create')).toHaveLength(1))

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


})

interface MockHostOptions {
  pickedPath?: string | null
  workspaces?: WorkspaceListValue
  workspaceFailure?: string
  credentialRef?: string
  settingsFailure?: string
  missingProvider?: boolean
  pendingRestart?: boolean
  existingSession?: boolean
  createFailure?: string
  createWait?: Promise<void>
  historyEvents?: SessionEvent[]
  historyHasMore?: boolean
  historyPages?: Record<string, { events: SessionEvent[], hasMore: boolean }>
  historyWait?: Promise<void>
  listWait?: Promise<void>
  sessionPages?: Record<string, { items: SessionSummary[], nextCursor?: string }>
}

function installMockHost(options: MockHostOptions = {}): {
  methods: string[]
  responses: unknown[]
  calls: Array<{ method: string, payload: unknown }>
} {
  let sessions = options.existingSession === true ? [session] : []
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
    case 'directoryPicker.pick':
      value = options.pickedPath ?? null
      break
    case 'workspace.list':
      value = options.workspaces ?? { items: [], archivedSessionIds: [] }
      break
    case 'workspace.create':
      if (options.workspaceFailure !== undefined) return response({ rpcId: call.rpcId, result: { ok: false, error: { message: options.workspaceFailure } } })
      value = { workspace: options.workspaces?.items[0], created: true }
      break
    case 'host.describe':
      value = {
        version: 'test',
        cwd: '/workspace',
        model: 'mock',
        attachedSessions: 0,
        canOpenPath: false,
      }
      break
    case 'settings.describe':
      if (options.settingsFailure !== undefined) return response({ rpcId: call.rpcId, result: { ok: false, error: { message: options.settingsFailure } } })
      value = { writable: true, namespaces: options.missingProvider ? [] : [{
        ns: 'llmprovider', name: 'deepseek', value: options.credentialRef ? { apiKeyEnv: options.credentialRef } : {},
        revision: 0, applies: 'restart', pendingRestart: options.pendingRestart,
      }] }
      break
    case 'credentials.set':
    case 'credentials.unset':
      value = {}
      break
    case 'credentials.describe':
      value = {
        credentials: {
          [options.credentialRef ?? 'DEEPSEEK_API_KEY']: {
            configured: true,
            source: 'environment',
            writable: false,
          },
        },
      }
      break
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
    case 'session.cancel':
      value = { accepted: true }
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

function workspaceListing(): WorkspaceListValue {
  return { items: [{ workspaceId: 'b', path: '/b', title: 'B', sessionIds: [] }], archivedSessionIds: [] }
}
