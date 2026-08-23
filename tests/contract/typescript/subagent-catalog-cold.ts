import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

void (async () => {
  const sourceRoot = resolve(process.argv[2])
  const expectedCommit = process.argv[3]
  const sourceCommit = execFileSync('git', ['rev-parse', 'HEAD'], {
    cwd: sourceRoot,
    encoding: 'utf8',
  }).trim()
  if (sourceCommit !== expectedCommit) {
    throw new Error(`subagent source commit ${sourceCommit} does not match ${expectedCommit}`)
  }

  const cordisModule = await import(pathToFileURL(resolve(
    sourceRoot,
    'vendor/cordis/src/index.ts',
  )).href)
  const sessionModule = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/core/session/src/index.ts',
  )).href)
  const projectionModule = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/session/session-projection/src/index.ts',
  )).href)
  const subagentModule = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/subagent/subagent/src/index.ts',
  )).href)

  const { Context } = cordisModule
  const { SessionId } = sessionModule
  const ctx = new Context()
  await ctx.plugin(sessionModule.default)
  await ctx.plugin(projectionModule.default)
  await ctx.plugin(subagentModule.default)

  const rootId = SessionId('catalog-cold-root')
  const header = (id: string, createdAt: number, parentSession = rootId) => ({
    version: sessionModule.SESSION_FORMAT_VERSION,
    id: SessionId(id),
    createdAt,
    parentSession,
    origin: 'subagent' as const,
  })
  const descriptor = (
    label: string,
    mode: 'one-shot' | 'continuable' = 'continuable',
  ) => ({
    version: subagentModule.SUBAGENT_DESCRIPTOR_VERSION,
    mode,
    provider: 'spawn',
    label,
  })
  const event = (seq: number, data: unknown) => ({
    type: 'subagent/descriptor' as const,
    seq,
    time: seq + 1,
    data,
  })

  const healthyHeader = header('cold-healthy', 1)
  const oneShotHeader = header('cold-one-shot', 2)
  const missingHeader = header('cold-missing', 3)
  const invalidHeader = header('cold-invalid', 4)
  const unavailableHeader = header('cold-unavailable', 5)
  const replacedHeader = header('cold-replaced', 6)
  const repeatedHeader = header('cold-repeated', 7)
  const staleLiveHeader = header('live-preferred', 8, SessionId('stale-parent'))
  const grandchildHeader = header('cold-grandchild', 9, healthyHeader.id)
  const headers = [
    grandchildHeader,
    staleLiveHeader,
    repeatedHeader,
    replacedHeader,
    unavailableHeader,
    invalidHeader,
    missingHeader,
    oneShotHeader,
    healthyHeader,
  ]
  const inspections = new Map([
    [healthyHeader.id, { meta: healthyHeader, events: [event(0, descriptor('healthy'))] }],
    [oneShotHeader.id, { meta: oneShotHeader, events: [event(0, descriptor('terminal', 'one-shot'))] }],
    [missingHeader.id, { meta: missingHeader, events: [] }],
    [invalidHeader.id, {
      meta: invalidHeader,
      events: [
        event(0, descriptor('was valid')),
        event(1, {
          version: subagentModule.SUBAGENT_DESCRIPTOR_VERSION,
          mode: 'continuable',
          provider: 7,
        }),
      ],
    }],
    [replacedHeader.id, {
      meta: {
        ...replacedHeader,
        createdAt: replacedHeader.createdAt + 100,
      },
      events: [event(0, descriptor('wrong lifecycle'))],
    }],
    [repeatedHeader.id, {
      meta: repeatedHeader,
      events: [
        event(0, descriptor('first')),
        event(1, descriptor('last')),
      ],
    }],
    [grandchildHeader.id, {
      meta: grandchildHeader,
      events: [event(0, descriptor('nested'))],
    }],
  ])
  ctx.provide('sessionPersistence', {
    list: async () => headers,
    inspect: async (id: string) => {
      if (id === unavailableHeader.id) throw new Error('unavailable')
      const inspection = inspections.get(SessionId(id))
      if (inspection === undefined) throw new Error(`missing inspection ${id}`)
      return inspection
    },
  })

  ctx.sessions.create(rootId)
  const live = ctx.sessions.create(staleLiveHeader.id, {
    meta: {
      parentSession: rootId,
      origin: 'subagent',
      createdAt: 8,
    },
  })
  live.append('subagent/descriptor', descriptor('live wins'))

  const entries = await ctx.subagents.listChildren(rootId)
  const abortController = new AbortController()
  abortController.abort()
  let cancelled: string | undefined
  try {
    await ctx.subagents.listChildren(rootId, abortController.signal)
  } catch (error: unknown) {
    cancelled = error instanceof subagentModule.SubagentError ? error.code : 'unexpected'
  }
  process.stdout.write(JSON.stringify({ entries, cancelled }))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
