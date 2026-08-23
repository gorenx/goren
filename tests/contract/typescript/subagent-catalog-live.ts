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

  const descriptor = (label: string) => ({
    version: subagentModule.SUBAGENT_DESCRIPTOR_VERSION,
    mode: 'continuable',
    provider: 'spawn',
    label,
  })
  const createChild = (
    id: string,
    parentId: string,
    createdAt: number,
    payload?: unknown,
  ) => {
    const child = ctx.sessions.create(SessionId(id), {
      meta: {
        parentSession: SessionId(parentId),
        origin: 'subagent',
        createdAt,
      },
    })
    if (payload !== undefined) child.append('subagent/descriptor', payload)
    return child
  }

  const rootId = SessionId('catalog-live-root')
  ctx.sessions.create(rootId)
  ctx.sessions.create(SessionId('ordinary-child'), {
    meta: {
      parentSession: rootId,
      createdAt: 1,
    },
  })
  createChild('creation-window', rootId, 0)
  const tieB = createChild('tie-b', rootId, 5, descriptor('tie b'))
  createChild('tie-a', rootId, 5, descriptor('tie a'))
  createChild('grandchild', tieB.id, 6, descriptor('nested'))
  createChild('late-one-shot', rootId, 9, {
    version: subagentModule.SUBAGENT_DESCRIPTOR_VERSION,
    mode: 'one-shot',
    provider: 'spawn',
    label: 'terminal',
  })
  const repeated = createChild('repeated', rootId, 10, descriptor('first'))
  repeated.append('subagent/descriptor', descriptor('last'))
  for (const id of [
    '_tie',
    '-tie',
    'a-tie',
    'A-tie',
    'ä-tie',
    'B-tie',
    'child-10',
    'child-2',
    'e\u0301-tie',
    'é-tie',
  ]) {
    createChild(id, rootId, 11, descriptor(id))
  }

  const entries = await ctx.subagents.listChildren(rootId)
  process.stdout.write(JSON.stringify(entries))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
