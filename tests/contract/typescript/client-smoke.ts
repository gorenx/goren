import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

type StreamResult = { done?: boolean; value?: { rpcId: string; payload: { type: string } } }

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
      host: { describe(payload: object): Promise<unknown> }
      events: {
        mux(payload: object, signal: AbortSignal, onOpen: () => void): AsyncIterable<unknown>
        host(payload: object, signal: AbortSignal, onOpen: () => void): AsyncIterable<unknown>
      }
      respond(message: object): Promise<unknown>
    }
  }

  const apiClient = new WebApiClient(3_000)
  const lifecycle = new AbortController()
  let muxOpened = false
  let hostOpened = false
  const muxIterator = apiClient.events.mux({}, lifecycle.signal, () => { muxOpened = true })[Symbol.asyncIterator]()
  const hostIterator = apiClient.events.host({}, lifecycle.signal, () => { hostOpened = true })[Symbol.asyncIterator]()

  const [description, muxResult, hostResult] = await Promise.all([
    apiClient.host.describe({}),
    muxIterator.next() as Promise<StreamResult>,
    hostIterator.next() as Promise<StreamResult>,
  ])
  if (muxResult.done === true || hostResult.done === true) throw new Error('Go event stream ended before its first frame')
  const receipt = await apiClient.respond({
    type: 'client-response',
    rpcId: 'not-pending',
    result: { ok: true },
  })

  lifecycle.abort()
  await Promise.allSettled([muxIterator.return?.(), hostIterator.return?.()])
  process.stdout.write(`${JSON.stringify({
    description,
    muxOpened,
    hostOpened,
    mux: muxResult.value,
    host: hostResult.value,
    receipt,
  })}\n`)
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
