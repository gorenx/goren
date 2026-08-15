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
    throw new Error('agent-loop failure contract source does not match the pinned manifest')
  }

  const cordisModule = await import(pathToFileURL(resolve(sourceRoot, 'vendor/cordis/src/index.ts')).href)
  const agentModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/agent/src/index.ts')).href)
  const loopModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/agent-loop/src/index.ts')).href)
  const llmModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm/src/index.ts')).href)
  const sessionModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/session/src/index.ts')).href)
  const promptModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/system-prompt/src/index.ts')).href)
  const toolsModule = await import(pathToFileURL(resolve(sourceRoot, 'packages/core/tools/src/index.ts')).href)

  const { Context } = cordisModule
  const AgentRegistry = agentModule.default
  const AgentLoop = loopModule.default
  const { createUserMessage, LlmAdapter } = llmModule
  const SessionStore = sessionModule.default
  const { SessionId } = sessionModule
  const SystemPrompt = promptModule.default
  const ToolRuntime = toolsModule.default
  const { defineContentToolFixture, TOOL_RUNTIME_SCHEDULER } = toolsModule

  class ScriptAdapter extends LlmAdapter {
    requests = 0

    constructor(private readonly chunks: Record<string, unknown>[]) {
      super()
    }

    async * stream() {
      this.requests += 1
      for (const chunk of this.chunks) yield chunk
    }
  }

  const setup = async (adapter: ScriptAdapter, maxParallelToolCalls?: number) => {
    const ctx = new Context()
    await ctx.plugin(llmModule.default)
    await ctx.plugin(SessionStore)
    await ctx.plugin(SystemPrompt, {})
    await ctx.plugin(ToolRuntime, {})
    await ctx.plugin(AgentRegistry)
    await ctx.plugin(AgentLoop, {
      agents: [],
      ...maxParallelToolCalls === undefined ? {} : { maxParallelToolCalls },
    })
    ctx.llm.registerAdapter(['mock'], adapter)
    return ctx
  }

  const waitForIdle = (ctx: InstanceType<typeof Context>, subject: unknown): Promise<void> => new Promise((accept) => {
    const dispose = ctx.on('agent/status', ({ agent, status }: { agent: unknown; status: string }) => {
      if (agent === subject && status === 'idle') {
        dispose()
        accept()
      }
    })
  })

  const send = (subject: { followup(message: unknown): void }) => subject.followup(createUserMessage({
    content: [{ type: 'text', text: 'run' }], source: { kind: 'user' },
  }))

  const project = (subject: { session: { events: { type: string; data: Record<string, unknown> }[] } }, requests: number) => {
    const events = subject.session.events
    const end = events.findLast(event => event.type === 'turn/end')
    return {
      eventTypes: events.map(event => event.type),
      turnEnd: (end?.data as { reason?: unknown } | undefined)?.reason,
      requestCount: requests,
    }
  }

  const preStepAdapter = new ScriptAdapter([
    { type: 'finish', reason: { kind: 'stop' } },
  ])
  const preStepContext = await setup(preStepAdapter)
  const preStepAgent = preStepContext.agentLoop.create(
    SessionId('failure-pre-step'), { provider: 'mock', model: 'model' },
  )
  preStepContext.on('agent/pre-step', async () => ({ kind: 'reject' }))
  const preStepIdle = waitForIdle(preStepContext, preStepAgent)
  send(preStepAgent)
  await preStepIdle
  const preStepReject = project(preStepAgent, preStepAdapter.requests)
  await preStepContext.fiber.dispose()

  const modelAdapter = new ScriptAdapter([
    { type: 'finish', reason: { kind: 'error', failure: { message: 'model failed', code: 'MODEL_FAILURE' } } },
  ])
  const modelContext = await setup(modelAdapter)
  const modelAgent = modelContext.agentLoop.create(
    SessionId('failure-model'), { provider: 'mock', model: 'model' },
  )
  const modelIdle = waitForIdle(modelContext, modelAgent)
  send(modelAgent)
  await modelIdle
  const modelFailure = project(modelAgent, modelAdapter.requests)
  await modelContext.fiber.dispose()

  const schedulerAdapter = new ScriptAdapter([
    { type: 'block-end', index: 0, block: { type: 'tool-call', id: 'call-1', name: 'parallel', arguments: '{"id":"1"}' } },
    { type: 'block-end', index: 1, block: { type: 'tool-call', id: 'call-2', name: 'parallel', arguments: '{"id":"2"}' } },
    { type: 'block-end', index: 2, block: { type: 'tool-call', id: 'call-3', name: 'parallel', arguments: '{"id":"3"}' } },
    { type: 'finish', reason: { kind: 'tool-calls' } },
  ])
  const schedulerContext = await setup(schedulerAdapter, 2)
  const secondStarted = Promise.withResolvers<void>()
  const releaseSecond = Promise.withResolvers<void>()
  const failureReturned = Promise.withResolvers<void>()
  const started: string[] = []
  schedulerContext.tools.register(defineContentToolFixture({
    name: 'parallel',
    description: 'parallel failure fixture',
    parameters: { id: { type: 'string', required: true } },
    isConcurrencySafe: () => true,
    async execute(args: { id: string }) {
      started.push(args.id)
      if (args.id === '2') {
        secondStarted.resolve()
        await releaseSecond.promise
      }
      return [{ type: 'text', text: args.id }]
    },
  }))
  const staged = schedulerContext.tools[TOOL_RUNTIME_SCHEDULER]
  const originalDispatch = staged.dispatch.bind(staged)
  staged.dispatch = async (execution: { callId: string }) => {
    if (execution.callId !== 'call-1') return originalDispatch(execution)
    await secondStarted.promise
    failureReturned.resolve()
    throw new Error('scheduler failed')
  }
  const schedulerAgent = schedulerContext.agentLoop.create(
    SessionId('failure-scheduler'), { provider: 'mock', model: 'model' },
  )
  let becameIdle = false
  const schedulerIdle = waitForIdle(schedulerContext, schedulerAgent).then(() => { becameIdle = true })
  send(schedulerAgent)
  await failureReturned.promise
  await new Promise<void>(accept => setImmediate(accept))
  const idleBeforeRelease = becameIdle
  releaseSecond.resolve()
  await schedulerIdle
  const schedulerFailure = {
    ...project(schedulerAgent, schedulerAdapter.requests),
    started,
    idleBeforeRelease,
    toolCallIds: schedulerAgent.session.events
      .filter((event: { type: string }) => event.type === 'tool/call')
      .map((event: { data: { callId: string } }) => event.data.callId),
    toolResultCount: schedulerAgent.session.events
      .filter((event: { type: string }) => event.type === 'tool/result').length,
  }
  await schedulerContext.fiber.dispose()

  process.stdout.write(JSON.stringify({ preStepReject, modelFailure, schedulerFailure }))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
