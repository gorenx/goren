import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

type HTTPCase = {
  name: string
  method: string
  path: string
  contentType?: string
  body?: string
}

void (async () => {
  const sourceRoot = resolve(process.argv[2])
  let encodedCases = ''
  for await (const chunk of process.stdin) encodedCases += String(chunk)
  const cases = JSON.parse(encodedCases) as HTTPCase[]
  const handlerURL = pathToFileURL(resolve(
    sourceRoot,
    'packages/host/apiproxy/src/fetch/handler.ts',
  )).href
  const { toFetchHandler } = await import(handlerURL)
  const api = {
    host: {
      describe: async (request: { rpcId: string }) => {
        if (request.rpcId === 'crash') throw new Error('dependency failed')
        return {
          rpcId: request.rpcId,
          result: {
            ok: true,
            value: {
              version: '0.1.0-rc.5', cwd: '/contract-workspace', attachedSessions: 0, canOpenPath: false,
            },
          },
        }
      },
    },
    respond: async () => ({ accepted: false, reason: 'not-pending' }),
  }
  const handler = toFetchHandler(api)
  const observations = []
  for (const testCase of cases) {
    const headers = testCase.contentType === undefined ? undefined : { 'content-type': testCase.contentType }
    const request = new Request(new URL(testCase.path, 'http://dsh.internal'), {
      method: testCase.method,
      ...headers === undefined ? {} : { headers },
      ...testCase.body === undefined ? {} : { body: testCase.body },
    })
    const response = await handler.fetch(request)
    observations.push({
      name: testCase.name,
      status: response.status,
      contentType: response.headers.get('content-type') ?? '',
      body: await response.text(),
    })
  }
  process.stdout.write(`${JSON.stringify(observations)}\n`)
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
