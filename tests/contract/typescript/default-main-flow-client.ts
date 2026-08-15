import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { createFrameMatcher } from './stream-matcher.ts'

type Result<T> = { rpcId: string; result: { ok: true; value: T } | { ok: false; error: unknown } }
type Frame = { rpcId: string; payload: { type: string; [key: string]: unknown } }

let stopStreams: (() => Promise<void>) | undefined

void (async () => {
  const sourceRoot = resolve(process.argv[2])
  const baseURL = process.argv[3]
  const cwd = process.argv[4]
  if (baseURL === undefined || cwd === undefined) {
    throw new Error('Go host URL and cwd are required')
  }

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
        create(payload: object): Promise<Result<{ sessionId: string }>>
        history(payload: object): Promise<Result<{
          events: Array<{ event: { type: string } }>
        }>>
        models(payload: object): Promise<Result<{
          current: { provider: string; model: string }
          routable: boolean
        }>>
        prompt(payload: object): Promise<Result<{ accepted: true }>>
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
  const eventType = (frame: Frame, expected: string): boolean => {
    if (frame.payload.type !== 'session/event') return false
    return (frame.payload.event as { type?: string } | undefined)?.type === expected
  }

  const apiClient = new WebApiClient(5_000)
  const lifecycle = new AbortController()
  let resolveMuxOpen!: () => void
  let resolveHostOpen!: () => void
  const muxOpen = new Promise<void>(resolveOpen => { resolveMuxOpen = resolveOpen })
  const hostOpen = new Promise<void>(resolveOpen => { resolveHostOpen = resolveOpen })
  const muxIterator = apiClient.events.mux({}, lifecycle.signal, resolveMuxOpen)[Symbol.asyncIterator]()
  const hostIterator = apiClient.events.host({}, lifecycle.signal, resolveHostOpen)[Symbol.asyncIterator]()
  stopStreams = async () => {
    lifecycle.abort()
    await Promise.allSettled([muxIterator.return?.(), hostIterator.return?.()])
  }
  const firstMux = muxIterator.next()
  const firstHost = hostIterator.next()
  await Promise.all([muxOpen, hostOpen])

  const sessionId = 'default-main-flow-contract'
  const created = unwrap(await apiClient.sessions.create({ sessionId, cwd }))
  const subscribed = await firstMux
  const added = await firstHost
  if (subscribed.done === true || subscribed.value.payload.type !== 'session/subscribed') {
    throw new Error(`unexpected subscribed frame: ${JSON.stringify(subscribed)}`)
  }
  if (added.done === true || added.value.payload.type !== 'host/session-added') {
    throw new Error(`unexpected added frame: ${JSON.stringify(added)}`)
  }

  const describeFrame = (frame: Frame): string => {
    if (frame.payload.type !== 'session/event') return frame.payload.type
    const event = frame.payload.event as { type?: string; seq?: number }
    return `session/event:${event.type ?? '?'}:${event.seq ?? '?'}`
  }
  const nextMux = createFrameMatcher(muxIterator, {
    timeoutMs: 10_000, maxFrames: 200, describeFrame,
  })
  const nextHost = createFrameMatcher(hostIterator, {
    timeoutMs: 10_000, maxFrames: 200, describeFrame,
  })
  const models = unwrap(await apiClient.sessions.models({ sessionId }))
  const runningFrame = nextHost(
    frame => frame.payload.type === 'host/session-status'
      && frame.payload.sessionId === sessionId
      && frame.payload.running === true,
    'running status',
  )
  const turnEndFrame = nextMux(frame => eventType(frame, 'turn/end'), 'turn/end')
  const prompted = unwrap(await apiClient.sessions.prompt({
    sessionId,
    mode: 'queue',
    content: [{ type: 'text', text: 'hello from the fixed TypeScript client' }],
    clientTimeZone: 'UTC',
  }))
  await runningFrame
  await turnEndFrame
  const idle = await nextHost(
    frame => frame.payload.type === 'host/session-status'
      && frame.payload.sessionId === sessionId
      && frame.payload.running === false,
    'idle status',
  )
  const history = unwrap(await apiClient.sessions.history({ sessionId }))

  await stopStreams()
  stopStreams = undefined
  process.stdout.write(`${JSON.stringify({
    sessionId: created.sessionId,
    model: models.current,
    routable: models.routable,
    accepted: prompted.accepted,
    idle: idle.payload.running === false,
    historyTypes: history.events.map(entry => entry.event.type),
  })}\n`)
})().catch(async (error: unknown) => {
  await stopStreams?.()
  console.error(error)
  process.exitCode = 1
})
