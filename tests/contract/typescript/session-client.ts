import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

type Result<T> = { rpcId: string; result: { ok: true; value: T } | { ok: false; error: unknown } }
type Frame = { rpcId: string; payload: { type: string; [key: string]: unknown } }

void (async () => {
  const sourceRoot = resolve(process.argv[2])
  const baseURL = process.argv[3]
  if (baseURL === undefined) throw new Error('Go host URL is required')

  Object.defineProperty(globalThis, 'location', {
    configurable: true,
    value: { origin: baseURL },
  })
  const moduleURL = pathToFileURL(resolve(
    sourceRoot,
    'packages/client/connection/src/client/web-api-client.ts',
  )).href
  const { WebApiClient } = await import(moduleURL) as {
    WebApiClient: new (timeoutMs?: number) => {
      sessions: {
        list(payload: object): Promise<Result<{ items: Array<Record<string, unknown>> }>>
        create(payload: object): Promise<Result<{ sessionId: string }>>
        history(payload: object): Promise<Result<{ events: Array<{ event: { type: string; data: unknown } }>; hasMore: boolean }>>
        models(payload: object): Promise<Result<{ current: { provider: string; model: string }; routable: boolean; groups: unknown[]; failures: unknown[] }>>
        selectModel(payload: object): Promise<Result<{ selected: { provider: string; model: string } }>>
        prompt(payload: object): Promise<Result<{ accepted: true }>>
        updateQueue(payload: object): Promise<Result<{ accepted: true }>>
        cancel(payload: object): Promise<Result<{ accepted: true }>>
      }
      events: {
        mux(payload: object, signal: AbortSignal, onOpen: () => void): AsyncIterable<Frame>
        host(payload: object, signal: AbortSignal, onOpen: () => void): AsyncIterable<Frame>
      }
    }
  }

  const unwrap = <T>(response: Result<T>): T => {
    if (!response.result.ok) throw new Error(`RPC failed: ${JSON.stringify(response.result.error)}`)
    return response.result.value
  }
  const nextMatching = async (
    iterator: AsyncIterator<Frame>,
    predicate: (frame: Frame) => boolean,
    label: string,
  ): Promise<Frame> => {
    for (let index = 0; index < 200; index++) {
      const outcome = await Promise.race([
        iterator.next(),
        new Promise<never>((_resolve, reject) => setTimeout(() => reject(new Error(`${label} timed out`)), 5_000)),
      ])
      if (outcome.done === true) throw new Error(`${label}: stream ended`)
      if (predicate(outcome.value)) return outcome.value
    }
    throw new Error(`${label}: frame budget exhausted`)
  }
  const eventType = (frame: Frame, event: string): boolean => {
    if (frame.payload.type !== 'session/event') return false
    const committed = frame.payload.event as { type?: string } | undefined
    return committed?.type === event
  }
  const queueItem = (frame: Frame, text: string): { id: string } | undefined => {
    if (frame.payload.type !== 'session/queue') return undefined
    const items = frame.payload.items as Array<{
      id: string
      message: { content: Array<{ type: string; text?: string }> }
    }>
    return items.find(item => item.message.content.some(block => block.type === 'text' && block.text === text))
  }

  const apiClient = new WebApiClient(5_000)
  const lifecycle = new AbortController()
  let resolveMuxOpen!: () => void
  let resolveHostOpen!: () => void
  const muxOpen = new Promise<void>(resolveOpen => { resolveMuxOpen = resolveOpen })
  const hostOpen = new Promise<void>(resolveOpen => { resolveHostOpen = resolveOpen })
  const muxIterator = apiClient.events.mux({}, lifecycle.signal, resolveMuxOpen)[Symbol.asyncIterator]()
  const hostIterator = apiClient.events.host({}, lifecycle.signal, resolveHostOpen)[Symbol.asyncIterator]()
  const firstMux = muxIterator.next()
  const firstHost = hostIterator.next()
  await Promise.all([muxOpen, hostOpen])

  const sessionId = 'session-client-contract'
  const created = unwrap(await apiClient.sessions.create({ sessionId, cwd: '/contract-workspace' }))
  const subscribed = await firstMux
  const added = await firstHost
  if (subscribed.done === true || subscribed.value.payload.type !== 'session/subscribed') {
    throw new Error(`unexpected subscribe frame: ${JSON.stringify(subscribed)}`)
  }
  if (added.done === true || added.value.payload.type !== 'host/session-added') {
    throw new Error(`unexpected added frame: ${JSON.stringify(added)}`)
  }

  const presetAdoption = await apiClient.sessions.create({ sessionId, cwd: '/contract-workspace', agentPreset: 'coding' })
  const presetConflict = !presetAdoption.result.ok
    && (presetAdoption.result.error as { code?: string }).code === 'agent-preset-conflict'

  const listed = unwrap(await apiClient.sessions.list({}))
  const emptyHistory = unwrap(await apiClient.sessions.history({ sessionId }))
  const models = unwrap(await apiClient.sessions.models({ sessionId }))
  const selected = unwrap(await apiClient.sessions.selectModel({
    sessionId, provider: 'mock', model: 'mock-model',
  }))

  const firstPrompt = unwrap(await apiClient.sessions.prompt({
    sessionId, mode: 'queue', content: [{ type: 'text', text: 'first' }], clientTimeZone: 'UTC',
  }))
  await nextMatching(muxIterator, frame => eventType(frame, 'turn/end'), 'first turn/end')
  await nextMatching(
    hostIterator,
    frame => frame.payload.type === 'host/session-status' && frame.payload.running === false,
    'first idle status',
  )
  const firstHistory = unwrap(await apiClient.sessions.history({ sessionId }))

  const secondPrompt = unwrap(await apiClient.sessions.prompt({
    sessionId, mode: 'queue', content: [{ type: 'text', text: 'blocking' }],
  }))
  await nextMatching(
    hostIterator,
    frame => frame.payload.type === 'host/session-status' && frame.payload.running === true,
    'second running status',
  )
  unwrap(await apiClient.sessions.prompt({
    sessionId, mode: 'queue', content: [{ type: 'text', text: 'third' }],
  }))
  const queuedFrame = await nextMatching(muxIterator, frame => queueItem(frame, 'third') !== undefined, 'third queue')
  const queued = queueItem(queuedFrame, 'third')
  if (queued === undefined) throw new Error('queued item disappeared')
  unwrap(await apiClient.sessions.updateQueue({
    sessionId, itemId: queued.id,
    action: { kind: 'edit', content: [{ type: 'text', text: 'edited', extension: 1 }] },
  }))
  await nextMatching(muxIterator, frame => queueItem(frame, 'edited')?.id === queued.id, 'edited queue')
  unwrap(await apiClient.sessions.updateQueue({
    sessionId, itemId: queued.id, action: { kind: 'remove' },
  }))
  await nextMatching(muxIterator, frame => {
    if (frame.payload.type !== 'session/queue') return false
    const items = frame.payload.items as Array<{ id: string }>
    return !items.some(item => item.id === queued.id)
  }, 'removed queue')
  const cancelled = unwrap(await apiClient.sessions.cancel({ sessionId }))
  const aborted = await nextMatching(muxIterator, frame => {
    if (!eventType(frame, 'turn/end')) return false
    const committed = frame.payload.event as { data?: { reason?: { kind?: string } } }
    return committed.data?.reason?.kind === 'aborted'
  }, 'aborted turn')
  await nextMatching(
    hostIterator,
    frame => frame.payload.type === 'host/session-status' && frame.payload.running === false,
    'cancelled idle status',
  )
  const finalHistory = unwrap(await apiClient.sessions.history({ sessionId }))

  lifecycle.abort()
  await Promise.allSettled([muxIterator.return?.(), hostIterator.return?.()])
  process.stdout.write(`${JSON.stringify({
    created,
    presetConflict,
    listed: listed.items.some(item => item.sessionId === sessionId),
    emptyHistory: emptyHistory.events.length === 0,
    models: models.current.provider === 'mock' && models.current.model === 'mock-model'
      && models.routable && models.groups.length === 1 && models.failures.length === 0,
    selected: selected.selected.provider === 'mock' && selected.selected.model === 'mock-model',
    firstPrompt: firstPrompt.accepted,
    firstHistoryTypes: firstHistory.events.map(entry => entry.event.type),
    secondPrompt: secondPrompt.accepted,
    cancelled: cancelled.accepted,
    aborted: eventType(aborted, 'turn/end'),
    finalHistoryTypes: finalHistory.events.map(entry => entry.event.type),
  })}\n`)
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
