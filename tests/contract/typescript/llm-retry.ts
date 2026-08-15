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
    throw new Error('llm-retry contract source does not match the pinned manifest')
  }

  const cordisModule = await import(pathToFileURL(resolve(sourceRoot, 'vendor/cordis/src/index.ts')).href)
  const agentModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/agent/src/index.ts')).href)
  const loopModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/agent-loop/src/index.ts')).href)
  const llmModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm/src/index.ts')).href)
  const retryModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm-retry/src/index.ts')).href)
  const sessionModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/session/src/index.ts')).href)
  const promptModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/system-prompt/src/index.ts')).href)
  const toolsModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/tools/src/index.ts')).href)

  const { Context } = cordisModule
  const AgentRegistry = agentModule.default
  const AgentLoop = loopModule.default
  const { createUserMessage, LlmAdapter, LlmError, resolveRetryPolicy } = llmModule
  const SessionStore = sessionModule.default
  const { SessionId } = sessionModule
  const SystemPrompt = promptModule.default
  const ToolRuntime = toolsModule.default

  class RetryAdapter extends LlmAdapter {
    requests = 0
    private readonly policy = resolveRetryPolicy({
      mode: 'normal',
      maxRetries: 2,
      retryableCodes: ['SERVER', 'RATE_LIMIT'],
      backoff: { initialDelayMs: 1, maxDelayMs: 4, jitterRatio: 0.5 },
    }, 'contract provider retryPolicy')

    override providerRetryPolicy() {
      return this.policy
    }

    async * stream() {
      this.requests += 1
      if (this.requests === 1) {
        throw new LlmError('busy', 'RATE_LIMIT', { status: 429 })
      }
      if (this.requests === 2) {
        throw new LlmError('retry later', 'SERVER', { providerRetryAfterMs: 3 })
      }
      yield { type: 'block-end', index: 0, block: { type: 'text', text: 'recovered' } }
      yield { type: 'finish', reason: { kind: 'stop' } }
    }
  }

  const ctx = new Context()
  await ctx.plugin(llmModule.default)
  await ctx.plugin(SessionStore)
  await ctx.plugin(SystemPrompt, {})
  await ctx.plugin(ToolRuntime, {})
  await ctx.plugin(AgentRegistry)
  await ctx.plugin(Object.assign((inner: InstanceType<typeof Context>) => {
    retryModule.apply(inner, {}, { random: () => 0 })
  }, { inject: retryModule.inject }))
  await ctx.plugin(AgentLoop, { agents: [] })

  const adapter = new RetryAdapter()
  ctx.llm.registerAdapter(['mock'], adapter)
  const subject = ctx.agentLoop.create(SessionId('retry-contract'), {
    provider: 'mock', model: 'model',
  })
  subject.followup(createUserMessage({
    content: [{ type: 'text', text: 'recover' }], source: { kind: 'user' },
  }))
  await subject.whenIdle()

  const chainLabels = new Map<string, string>()
  const normalizeId = (identifier: string) => {
    let label = chainLabels.get(identifier)
    if (label === undefined) {
      label = `chain-${chainLabels.size + 1}`
      chainLabels.set(identifier, label)
    }
    return label
  }
  const retryEvents = subject.session.events
    .filter(event => event.type === 'llm/retry' || event.type === 'llm/retry-started')
    .map(event => ({
      type: event.type,
      data: { ...event.data, retryId: normalizeId(event.data.retryId) },
    }))
  const roles = subject.session.deriveMessages().map(message => message.role)
  const finalMessage = subject.session.deriveMessages().at(-1)
  const finalText = finalMessage?.content[0]?.type === 'text' ? finalMessage.content[0].text : null

  process.stdout.write(JSON.stringify({
    retryEvents,
    requestCount: adapter.requests,
    roles,
    finalText,
  }))
  await ctx.fiber.dispose()
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
