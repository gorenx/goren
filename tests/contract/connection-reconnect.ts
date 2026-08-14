import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

void (async () => {
  const sourceRoot = resolve(process.argv[2])
  const baseURL = process.argv[3]
  if (baseURL === undefined) throw new Error('Go host URL is required')
  Object.defineProperty(globalThis, 'location', {
    configurable: true,
    value: { origin: baseURL },
  })

  const webClientURL = pathToFileURL(resolve(
    sourceRoot,
    'packages/client/connection/src/client/web-api-client.ts',
  )).href
  const controllerURL = pathToFileURL(resolve(
    sourceRoot,
    'packages/client/connection/src/client/connection.ts',
  )).href
  const [{ WebApiClient }, { ConnectionController }] = await Promise.all([
    import(webClientURL),
    import(controllerURL),
  ])

  let connectedCount = 0
  let muxCount = 0
  let hostCount = 0
  const states: string[] = []
  let settleReady: (() => void) | undefined
  let rejectReady: ((error: Error) => void) | undefined
  const ready = new Promise<void>((resolveReady, reject) => {
    settleReady = resolveReady
    rejectReady = reject
  })
  const apiClient = new WebApiClient(3_000)
  const controller = new ConnectionController(apiClient, {
    onMuxEnvelope: () => { muxCount += 1 },
    onHostEnvelope: () => { hostCount += 1 },
    onStateChange: (state: string) => { states.push(state) },
    onConnected: () => {
      connectedCount += 1
      if (connectedCount === 1) {
        void fetch(`${baseURL}/api/contract.release`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({
            type: 'client-request', rpcId: 'release-1', method: 'contract.release', payload: {},
          }),
        }).then((response) => {
          if (!response.ok) throw new Error(`release failed with HTTP ${response.status}`)
        }).catch((error: unknown) => {
          rejectReady?.(error instanceof Error ? error : new Error(String(error)))
        })
      }
      if (connectedCount === 2) {
        controller.stop()
        settleReady?.()
      }
    },
  }, {
    backoffBaseMs: 2,
    backoffFactor: 1,
    backoffMaxMs: 2,
    streamOpenTimeoutMs: 1_000,
  })

  const timeout = setTimeout(() => {
    controller.stop()
    rejectReady?.(new Error('ConnectionController did not establish a second generation'))
  }, 5_000)
  controller.start()
  await ready
  clearTimeout(timeout)
  process.stdout.write(`${JSON.stringify({ connectedCount, muxCount, hostCount, states })}\n`)
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
