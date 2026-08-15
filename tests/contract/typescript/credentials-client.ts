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
      credentials: {
        describe(payload: { refs: string[] }): Promise<unknown>
        set(payload: { ref: string, value: string }): Promise<unknown>
        unset(payload: { ref: string }): Promise<unknown>
      }
    }
  }

  const client = new WebApiClient(5_000)
  const ref = 'DEEPSEEK_API_KEY'
  await client.credentials.set({ ref, value: 'contract-test-credential' })
  const configured = await client.credentials.describe({ refs: [ref] })
  await client.credentials.unset({ ref })
  const removed = await client.credentials.describe({ refs: [ref] })
  process.stdout.write(`${JSON.stringify({ configured, removed })}\n`)
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
