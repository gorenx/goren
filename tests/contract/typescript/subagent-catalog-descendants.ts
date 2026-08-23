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

  const descriptor = (label: string, mode: 'one-shot' | 'continuable' = 'continuable') => ({
    version: subagentModule.SUBAGENT_DESCRIPTOR_VERSION,
    mode,
    provider: 'spawn',
    label,
  })
  const createSession = (
    id: string,
    parentId: string,
    createdAt: number,
    origin: boolean,
    payload?: unknown,
  ) => {
    const child = ctx.sessions.create(SessionId(id), {
      meta: {
        parentSession: SessionId(parentId),
        createdAt,
        ...(origin ? { origin: 'subagent' as const } : {}),
      },
    })
    if (payload !== undefined) child.append('subagent/descriptor', payload)
    return child
  }

  const rootId = SessionId('catalog-tree-root')
  ctx.sessions.create(rootId)
  const branchA = createSession('branch-a', rootId, 1, true, descriptor('branch a'))
  const ordinary = createSession('ordinary-middle', branchA.id, 2, false)
  const oneShot = createSession(
    'one-shot-middle',
    ordinary.id,
    3,
    true,
    descriptor('one shot', 'one-shot'),
  )
  const creationWindow = createSession('creation-window-middle', oneShot.id, 4, true)
  createSession('deep-leaf', creationWindow.id, 5, true, descriptor('deep leaf'))
  createSession('branch-b', rootId, 6, true, descriptor('branch b'))

  const entries = await ctx.subagents.listDescendants(rootId)
  process.stdout.write(JSON.stringify(entries))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
