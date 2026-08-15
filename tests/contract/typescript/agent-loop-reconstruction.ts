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
    throw new Error('agent-loop reconstruction source does not match the pinned manifest')
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
  const { foldRequestHeader, Session, SessionId } = sessionModule
  const SystemPrompt = promptModule.default
  const ToolRuntime = toolsModule.default

  class ReconstructionAdapter extends LlmAdapter {
    requests: Record<string, unknown>[] = []

    constructor(private readonly reply: string) {
      super()
    }

    async * stream(options: Record<string, unknown>) {
      this.requests.push(options)
      yield { type: 'block-end', index: 0, block: { type: 'text', text: this.reply } }
      yield { type: 'finish', reason: { kind: 'stop' } }
    }
  }

  const mount = async (reply: string) => {
    const ctx = new Context()
    await ctx.plugin(llmModule.default)
    await ctx.plugin(SessionStore)
    await ctx.plugin(SystemPrompt, {})
    await ctx.plugin(ToolRuntime, {})
    await ctx.plugin(AgentRegistry)
    await ctx.plugin(AgentLoop, { agents: [] })
    const adapter = new ReconstructionAdapter(reply)
    ctx.llm.registerAdapter(['mock'], adapter)
    return { ctx, adapter }
  }

  const waitForIdle = (ctx: InstanceType<typeof Context>, subject: object): Promise<void> =>
    new Promise((accept) => {
      const dispose = ctx.on('agent/status', ({ agent, status }: { agent: object; status: string }) => {
        if (agent === subject && status === 'idle') {
          dispose()
          accept()
        }
      })
    })

  const send = (subject: { followup(message: unknown): void }, text: string) => {
    subject.followup(createUserMessage({
      content: [{ type: 'text', text }],
      source: { kind: 'user' },
    }))
  }

  const projectMessage = (message: Record<string, unknown>) => ({
    role: message.role,
    content: message.content,
    source: message.source,
  })

  const projectRequest = (request: Record<string, unknown>) => ({
    provider: request.provider,
    model: request.model,
    ...request.reasoningEffort === undefined ? {} : { reasoningEffort: request.reasoningEffort },
    ...request.temperature === undefined ? {} : { temperature: request.temperature },
    ...request.maxTokens === undefined ? {} : { maxTokens: request.maxTokens },
    ...request.stop === undefined ? {} : { stop: request.stop },
    ...request.system === undefined ? {} : { system: request.system },
    tools: Array.isArray(request.tools)
      ? request.tools.map(tool => (tool as { name: string }).name)
      : [],
    messages: (request.messages as Record<string, unknown>[]).map(projectMessage),
    sessionId: request.sessionId,
  })

  const reconstruct = (
    identifier: string,
    events: { type: string; seq: number; data: Record<string, unknown> }[],
  ) => {
    const firstNewChunk = events.findLast(event => event.type === 'assistant/chunk')
    if (firstNewChunk === undefined) throw new Error(`session ${identifier} has no dispatch chunk`)
    const prefix = structuredClone(events.slice(0, firstNewChunk.seq))
    const rebuiltSession = Session.create(SessionId(`rebuild-${identifier}`), prefix)
    const folded = foldRequestHeader(prefix)
    if (folded === undefined) throw new Error(`session ${identifier} has no request header`)
    return projectRequest({
      ...folded.config,
      messages: rebuiltSession.deriveMessages(),
      ...folded.system === undefined ? {} : { system: folded.system },
      ...folded.tools === undefined ? {} : { tools: folded.tools },
      sessionId: identifier,
    })
  }

  const observe = (
    identifier: string,
    request: Record<string, unknown>,
    conversation: {
      events: { type: string; seq: number; data: Record<string, unknown> }[]
      surface: { replaceGeneration: number }
    },
  ) => ({
    request: projectRequest(request),
    rebuilt: reconstruct(identifier, conversation.events),
    headerReasons: conversation.events
      .filter(event => event.type === 'request/header')
      .map(event => (event.data as { reason: string }).reason),
    requestContextCount: conversation.events.filter(event => event.type === 'request/context').length,
    replaceGeneration: conversation.surface.replaceGeneration,
  })

  const first = await mount('one')
  const firstAgent = first.ctx.agentLoop.create(
    SessionId('reconstruction-first'),
    { provider: 'mock', model: 'model' },
  )
  send(firstAgent, 'first')
  await waitForIdle(first.ctx, firstAgent)

  const shadowed = [...firstAgent.session.surface.nodes]
  if (shadowed.length !== 2) throw new Error(`unexpected first surface size ${shadowed.length}`)
  firstAgent.session.append('user/message', createUserMessage({
    content: [{ type: 'text', text: '[summary of turn 1]' }],
    source: { kind: 'plugin', plugin: 'test-compact' },
  }), {
    surfaceOp: { op: 'replace', start: shadowed[0], end: shadowed[1] },
    sourceEventSeqs: shadowed,
  })
  const forkSeed = structuredClone(firstAgent.session.events)
  const firstObservation = observe(
    'reconstruction-first', first.adapter.requests[0]!, firstAgent.session,
  )

  const resumed = await mount('two')
  resumed.ctx.systemPrompt.section({ name: 'extra', order: 2, text: 'new guidance' })
  resumed.ctx.on('agent/request', async (_payload: unknown, next: () => Promise<Record<string, unknown>>) => ({
    ...await next(), temperature: 0.5, maxTokens: 99, stop: ['<END>'],
  }))
  const resumedHandle = await resumed.ctx.agents.create({
    sessionId: SessionId('reconstruction-resumed'),
    seed: forkSeed,
    agentOptions: { provider: 'mock', model: 'model' },
  })
  send(resumedHandle.agent, 'second')
  await waitForIdle(resumed.ctx, resumedHandle.agent)
  const resumedObservation = observe(
    'reconstruction-resumed', resumed.adapter.requests[0]!, resumedHandle.agent.session,
  )

  process.stdout.write(JSON.stringify({ generations: [firstObservation, resumedObservation] }))
  await first.ctx.fiber.dispose()
  await resumed.ctx.fiber.dispose()
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
