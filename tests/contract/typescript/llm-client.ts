import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

type Result<T> = { rpcId: string; result: { ok: true; value: T } | { ok: false; error: { code: string } } }

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
      llm: {
        providers(payload: object): Promise<Result<unknown>>
        models(payload: object): Promise<Result<unknown>>
      }
    }
  }

  const unwrap = <T>(response: Result<T>): T => {
    if (!response.result.ok) throw new Error(`RPC failed: ${JSON.stringify(response.result.error)}`)
    return response.result.value
  }
  const apiClient = new WebApiClient(5_000)
  const [providers, models] = await Promise.all([
    apiClient.llm.providers({}),
    apiClient.llm.models({}),
  ])
  process.stdout.write(`${JSON.stringify({
    providers: unwrap(providers),
    models: unwrap(models),
  })}\n`)
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
