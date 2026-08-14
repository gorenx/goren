import { execFileSync } from 'node:child_process'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

void (async () => {
  const sourceRoot = resolve(process.argv[2])
  const manifestPath = resolve(process.argv[3])
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8')) as {
    source: { commit: string; version: string }
  }
  const sourceCommit = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: sourceRoot, encoding: 'utf8' }).trim()
  const sourcePackage = JSON.parse(await readFile(resolve(sourceRoot, 'package.json'), 'utf8')) as { version: string }
  if (sourceCommit !== manifest.source.commit || sourcePackage.version !== manifest.source.version) {
    throw new Error('tools contract source does not match the pinned manifest')
  }

  const [{ Context }, promptModule, toolsModule, scopeModule] = await Promise.all([
    import(pathToFileURL(resolve(sourceRoot, 'vendor/cordis/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/core/system-prompt/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/core/tools/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/core/scope/src/index.ts')).href),
  ])
  const SystemPrompt = promptModule.default
  const Tools = toolsModule.default
  const { createScope, scopeOf } = scopeModule

  const ctx = new Context()
  await ctx.plugin(SystemPrompt)
  await ctx.plugin(Tools)

  const steps: string[] = []
  ctx.on('tools/pre-execute', async (_exec, next) => {
    steps.push('pre-before')
    const decision = await next()
    steps.push('pre-after')
    return decision
  })
  ctx.tools.guard(() => {
    steps.push('guard')
    return undefined
  })
  ctx.on('tools/execute', async (_exec, next) => {
    steps.push('execute-before')
    const result = await next()
    steps.push('execute-after')
    return result
  })
  ctx.on('tools/post-execute', async (_exec, _result, next) => {
    steps.push('post-before')
    const decision = await next()
    steps.push('post-after')
    return decision
  })
  ctx.on('tools/result', () => {
    steps.push('result')
  })

  const definition = (name: string, description: string) => ({
    name,
    description,
    parameters: { type: 'object' as const },
    output: {
      schema: { type: 'object' as const },
      render: (_args: unknown, value: unknown) => {
        if (name === 'pipeline') steps.push('render')
        return [{ type: 'text' as const, text: JSON.stringify(value) }]
      },
    },
    execute: async (args: unknown) => {
      if (name === 'pipeline') steps.push('body')
      return args
    },
  })

  ctx.tools.register(definition('alpha', 'global alpha'))
  ctx.tools.register(definition('beta', 'global beta'))
  ctx.tools.register(definition('pipeline', 'pipeline'))
  const success = await ctx.tools.execute({
    callId: 'call-1', name: 'pipeline', arguments: { value: 'ok' }, signal: new AbortController().signal,
  })
  const unknown = await ctx.tools.execute({
    callId: 'unknown-1', name: 'absent', arguments: {}, signal: new AbortController().signal,
  })

  const key = {}
  let child!: ReturnType<typeof createScope>
  await ctx.plugin(Object.assign((inner: InstanceType<typeof Context>) => {
    child = createScope(inner, key)
  }, { inject: ['tools'] }))
  child.ctx.tools.restrict({ allow: ['alpha', 'pipeline'] })
  child.ctx.tools.register(definition('beta', 'scoped beta'))
  child.ctx.tools.register(definition('gamma', 'scoped gamma'))
  child.ctx.on('tools/pre-execute', async (exec, next) => {
    if (exec.name === 'alpha') return { kind: 'deny', reason: 'policy denied' }
    return next()
  })
  const scopedSchemas = child.ctx.tools.schemas(scopeOf(child.ctx))
  const denied = await child.ctx.tools.execute({
    callId: 'deny-1', name: 'alpha', arguments: {}, agent: key,
    signal: new AbortController().signal,
  })
  await child.dispose()
  const afterDisposeSchemas = ctx.tools.schemas(key)

  const observe = (result: Awaited<ReturnType<typeof ctx.tools.execute>>) => ({
    isError: result.isError,
    ...result.isError ? { error: result.error, content: result.content } : { value: result.value, content: result.content },
  })
  process.stdout.write(JSON.stringify({
    globalSchemas: ctx.tools.schemas(),
    scopedSchemas,
    afterDisposeSchemas,
    success: observe(success),
    unknown: observe(unknown),
    denied: observe(denied),
    steps,
  }))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
