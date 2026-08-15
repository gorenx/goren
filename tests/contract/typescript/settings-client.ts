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
  const moduleURL = pathToFileURL(resolve(
    sourceRoot,
    'packages/client/connection/src/client/web-api-client.ts',
  )).href
  const { WebApiClient } = await import(moduleURL) as {
    WebApiClient: new (timeoutMs?: number) => {
      settings: { describe(payload: object): Promise<unknown> }
    }
  }

  const response = await new WebApiClient(5_000).settings.describe({})
  process.stdout.write(`${JSON.stringify(response)}\n`)
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
