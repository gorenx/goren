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
    throw new Error('system-prompt contract source does not match the pinned manifest')
  }

  const [{ Context }, promptModule, scopeModule] = await Promise.all([
    import(pathToFileURL(resolve(sourceRoot, 'vendor/cordis/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/core/system-prompt/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/core/scope/src/index.ts')).href),
  ])
  const { default: SystemPrompt, TOOL_ORDER_REST, renderPrompt, renderContextSnapshot } = promptModule
  const { createScope, scopeOf } = scopeModule

  const observe = (assembly: {
    sections: { name: string; text: string }[]
    contexts: { name: string; text: string }[]
    tools: { name: string; description: string; parameters: object }[]
    variables: Record<string, string | undefined>
  }) => ({
    sections: assembly.sections,
    contexts: assembly.contexts,
    tools: assembly.tools,
    variables: Object.fromEntries(Object.entries(assembly.variables).map(([name, value]) => [name, value ?? null])),
    prompt: renderPrompt(assembly),
    contextSnapshot: renderContextSnapshot(assembly),
  })

  const ctx = new Context()
  await ctx.plugin(SystemPrompt, {
    persona: 'Mode: {{mode}}.',
    toolOrder: ['todo', TOOL_ORDER_REST, 'bash'],
  })
  ctx.systemPrompt.section({ name: 'rules', order: 10, text: 'Be precise.' })
  ctx.systemPrompt.section({ name: 'cwd', order: 20, text: () => 'cwd: /tmp' })
  ctx.systemPrompt.context({ name: 'later', order: 20, text: 'context 2' })
  ctx.systemPrompt.context({ name: 'earlier', order: 10, text: 'context {{mode}}' })
  ctx.systemPrompt.variable('mode', () => 'normal')
  ctx.systemPrompt.tools(() => ({ schemas: [
    { name: 'bash', description: 'shell', parameters: { type: 'object' } },
    { name: 'zeta', description: 'z', parameters: {} },
    { name: 'todo', description: 'tasks', parameters: {} },
    { name: 'alpha', description: 'a', parameters: {} },
  ] }))

  const key = {}
  let child!: ReturnType<typeof createScope>
  await ctx.plugin(Object.assign((inner: InstanceType<typeof Context>) => {
    child = createScope(inner, key)
  }, { inject: ['systemPrompt'] }))
  child.ctx.systemPrompt.section({ name: 'deployment:persona', order: 0, text: 'Scoped {{mode}}.' })
  child.ctx.systemPrompt.section({ name: 'child', order: 15, text: 'Child section.' })
  child.ctx.systemPrompt.variable('mode', () => 'strict')
  child.ctx.systemPrompt.tools(() => ({ schemas: [{ name: 'scoped', description: 's', parameters: {} }] }))
  child.ctx.on('system-prompt/assemble', async (assembly: {
    sections: { name: string; text: string }[]
  }, _context: unknown, next: () => Promise<unknown>) => {
    assembly.sections.push({ name: 'listener', text: 'Scoped listener.' })
    return next()
  })

  const global = observe(await ctx.systemPrompt.assemble())
  const scoped = observe(await ctx.systemPrompt.assemble({ scope: scopeOf(child.ctx) }))
  await child.dispose()
  const afterDispose = observe(await ctx.systemPrompt.assemble({ scope: key }))

  const completeContext = new Context()
  await completeContext.plugin(SystemPrompt)
  completeContext.systemPrompt.section({ name: 'complete', order: 50, text: 'Complete prompt.', complete: true })
  completeContext.on('system-prompt/assemble', async (assembly, _context, next) => {
    assembly.sections.push({ name: 'late', text: 'Late section.' })
    return next()
  })
  const complete = observe(await completeContext.systemPrompt.assemble())

  const suppressedContext = new Context()
  await suppressedContext.plugin(SystemPrompt, { includeRuntimeContext: false })
  let suppressedProviderCalls = 0
  suppressedContext.systemPrompt.context({
    name: 'policy', order: 1, text: () => `policy ${++suppressedProviderCalls}`,
  })
  suppressedContext.on('system-prompt/assemble', async (assembly, _context, next) => {
    assembly.contexts.push({ name: 'late', text: 'Late context.' })
    return next()
  })
  const suppressed = observe(await suppressedContext.systemPrompt.assemble())

  const renderCase = (text: string, variables: Record<string, string | undefined>) => {
    try {
      return { ok: true, value: renderPrompt({
        sections: [{ name: 'fixture', text }], contexts: [], tools: [], variables,
      }) }
    } catch {
      return { ok: false }
    }
  }
  const rendering = {
    singlePass: renderCase('Mode {{mode}}.', { mode: '{{other}}' }),
    loneOpen: renderCase('literal {{ prose', {}),
    unknown: renderCase('{{other}}', {}),
    undefined: renderCase('{{unset}}', { unset: undefined }),
    malformed: renderCase('{{bad-name}}', {}),
  }

  process.stdout.write(JSON.stringify({
    global, scoped, afterDispose, complete, suppressed, suppressedProviderCalls, rendering,
  }))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
