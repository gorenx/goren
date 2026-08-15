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
    throw new Error('agent-loop contract source does not match the pinned manifest')
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
  const { defineContentToolFixture } = toolsModule

  class ContractAdapter extends LlmAdapter {
    requests: Record<string, unknown>[] = []
    private index = 0

    async * stream(options: Record<string, unknown>) {
      this.requests.push(options)
      if (this.index++ === 0) {
        yield {
          type: 'block-end', index: 0,
          block: { type: 'tool-call', id: 'call-1', name: 'echo', arguments: '{"value":"hello"}' },
        }
        yield { type: 'finish', reason: { kind: 'tool-calls' } }
        return
      }
      yield { type: 'block-end', index: 0, block: { type: 'text', text: 'done' } }
      yield { type: 'finish', reason: { kind: 'stop' } }
    }
  }

  const projectMessage = (message: Record<string, unknown>) => ({
    role: message.role,
    content: message.content,
    source: message.source,
  })
  const projectHeader = (header: Record<string, unknown>) => ({
    config: header.config,
    ...header.adapterDefaults === undefined ? {} : { adapterDefaults: header.adapterDefaults },
    ...header.system === undefined ? {} : { system: header.system },
    tools: Array.isArray(header.tools)
      ? header.tools.map(tool => (tool as { name: string }).name)
      : [],
  })
  const projectEvent = (event: { type: string; seq: number; data: Record<string, unknown> }) => {
    if (event.type === 'agent/inbox/spliced') {
      const data = event.data as {
        target: string; start: number; removedCount?: number; outcome?: string;
        inserted: Record<string, unknown>[]
      }
      return {
        type: event.type, seq: event.seq,
        data: {
          target: data.target, start: data.start,
          ...data.removedCount === undefined ? {} : { removedCount: data.removedCount },
          inserted: data.inserted.map(projectMessage),
          ...data.outcome === undefined ? {} : { outcome: data.outcome },
        },
      }
    }
    if (event.type === 'user/message') {
      return { type: event.type, seq: event.seq, data: projectMessage(event.data) }
    }
    if (event.type === 'assistant/message' || event.type === 'tool/result') {
      const data = event.data as Record<string, unknown> & { message: Record<string, unknown> }
      return {
        type: event.type, seq: event.seq,
        data: {
          turn: data.turn, step: data.step, message: projectMessage(data.message),
          ...data.usage === undefined ? {} : { usage: data.usage },
          ...data.error === undefined ? {} : { error: data.error },
          ...data.meta === undefined ? {} : { meta: data.meta },
        },
      }
    }
    if (event.type === 'request/header') {
      const data = event.data as { header: Record<string, unknown>; reason: string }
      return { type: event.type, seq: event.seq, data: { header: projectHeader(data.header), reason: data.reason } }
    }
    return { type: event.type, seq: event.seq, data: event.data }
  }
  const projectRequest = (request: Record<string, unknown>) => ({
    provider: request.provider,
    model: request.model,
    ...request.system === undefined ? {} : { system: request.system },
    tools: Array.isArray(request.tools)
      ? request.tools.map(tool => (tool as { name: string }).name)
      : [],
    messages: (request.messages as Record<string, unknown>[]).map(projectMessage),
    sessionId: request.sessionId,
  })

  const ctx = new Context()
  await ctx.plugin(llmModule.default)
  await ctx.plugin(SessionStore)
  await ctx.plugin(SystemPrompt, {})
  await ctx.plugin(ToolRuntime, {})
  await ctx.plugin(AgentRegistry)
  await ctx.plugin(AgentLoop, { agents: [] })
  const adapter = new ContractAdapter()
  ctx.llm.registerAdapter(['mock'], adapter)
  ctx.tools.register(defineContentToolFixture({
    name: 'echo', description: 'echo one object', parameters: { value: { type: 'string' } },
    execute: () => Promise.resolve([{ type: 'text', text: '{"value":"hello"}' }]),
  }))
  const subject = ctx.agentLoop.create(SessionId('agent-loop-contract'), { provider: 'mock', model: 'model' })
  const idle = new Promise<void>((accept) => {
    const dispose = ctx.on('agent/status', ({ agent, status }: { agent: unknown; status: string }) => {
      if (agent === subject && status === 'idle') {
        dispose()
        accept()
      }
    })
  })
  subject.followup(createUserMessage({
    content: [{ type: 'text', text: 'hello' }], source: { kind: 'user' },
  }))
  await idle

  process.stdout.write(JSON.stringify({
    events: subject.session.events.map(projectEvent),
    requests: adapter.requests.map(projectRequest),
    derived: subject.session.deriveMessages().map(projectMessage),
  }))
  await ctx.fiber.dispose()
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
