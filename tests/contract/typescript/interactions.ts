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
    throw new Error('interactions contract source does not match the pinned manifest')
  }

  const [cordisModule, sessionModule, promptModule, toolsModule, approvalModule, questionsModule, askToolModule] = await Promise.all([
    import(pathToFileURL(resolve(sourceRoot, 'vendor/cordis/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/core/session/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/core/system-prompt/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/core/tools/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/interaction/user-approval/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/interaction/user-questions/src/index.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/interaction/tool-ask-user/src/index.ts')).href),
  ])
  const { Context } = cordisModule
  const { Session, SessionId } = sessionModule
  const SystemPrompt = promptModule.default
  const ToolRuntime = toolsModule.default
  const ApprovalService = approvalModule.default
  const UserQuestions = questionsModule.default

  const ctx = new Context()
  await ctx.plugin(SystemPrompt)
  await ctx.plugin(ToolRuntime)
  await ctx.plugin(ApprovalService)
  await ctx.plugin(UserQuestions)
  await ctx.plugin(askToolModule)

  const conversation = Session.create(SessionId('interaction-contract'))
  conversation.append('turn/start', { turn: 1 })
  const injected: Array<Record<string, unknown>> = []
  const agent = {
    id: 'interaction-contract',
    session: conversation,
    inject(message: Record<string, unknown>) {
      injected.push({ content: message.content, source: message.source })
    },
  }
  const unavailable = await ctx.approval.request({
    agent, toolName: 'shell', callId: 'call-1', reason: 'needs permission',
  })
  ctx.on('approval/request', () => Promise.resolve('allowed-once'))
  const allowed = await ctx.approval.request({ agent, toolName: 'read' })
  ctx.approval.setPolicy(agent, 'never')
  const rejected = await ctx.approval.request({ agent, toolName: 'write' })
  const policyContext = (await ctx.systemPrompt.assemble({ agent })).contexts
    .find((entry: { name: string }) => entry.name === 'approval:policy')?.text

  const approvalEvents = conversation.events.filter((entry: { type: string }) =>
    entry.type === 'approval/asked' || entry.type === 'approval/decided')
  const audit = []
  for (let index = 0; index < approvalEvents.length; index += 2) {
    const asked = approvalEvents[index] as { data: Record<string, unknown> }
    const decided = approvalEvents[index + 1] as { data: Record<string, unknown> }
    audit.push({
      asked: {
        toolName: asked.data.toolName,
        ...asked.data.callId === undefined ? {} : { callId: asked.data.callId },
        ...asked.data.reason === undefined ? {} : { reason: asked.data.reason },
      },
      decided: { outcome: decided.data.outcome, idMatches: decided.data.id === asked.data.id },
    })
  }

  let badIntent: Record<string, unknown> | undefined
  try {
    await ctx.userQuestions.ask({
      questions: [{
        id: 'plan', question: 'Approve?', detail: '# Plan',
        options: [{ label: 'Approve' }], intent: { kind: 'plan-review', approve: 'Ship it' },
      }],
    })
  } catch (error) {
    const failure = error as { name: string; code: string; message: string }
    badIntent = { name: failure.name, code: failure.code, message: failure.message }
  }

  const projectResult = (result: {
    isError: boolean; value?: unknown; error?: unknown; content: unknown[]
  }) => result.isError
    ? { isError: true, error: result.error, content: result.content }
    : { isError: false, value: result.value, content: result.content }
  const noProvider = projectResult(await ctx.tools.execute({
    callId: 'ask-none', name: 'ask_user_question',
    arguments: { questions: [{ id: 'continue', question: 'Continue?' }] },
    signal: new AbortController().signal,
  }))
  const seenQuestions: unknown[] = []
  ctx.userQuestions.registerProvider({
    async ask(request: { questions: unknown[] }) {
      seenQuestions.push(request.questions)
      return { answers: [{ id: 'targets', selected: ['tests', 'docs'], custom: 'release notes' }] }
    },
  })
  const answered = projectResult(await ctx.tools.execute({
    callId: 'ask-answered', name: 'ask_user_question',
    arguments: {
      questions: [{
        id: 'targets', question: 'What should I update?', header: 'Choose',
        options: [{ label: 'tests', description: 'Run tests.' }, { label: 'docs' }],
        multi_select: true,
      }],
    },
    signal: new AbortController().signal,
  }))

  process.stdout.write(JSON.stringify({
    approval: {
      outcomes: [unavailable, allowed, rejected], audit,
      override: ctx.approval.overrideOf(conversation), policyContext, injected,
    },
    questions: { badIntent, noProvider, seenQuestions, answered },
    toolSchema: ctx.tools.schemas().find((entry: { name: string }) => entry.name === 'ask_user_question'),
  }))
  await ctx.fiber.dispose()
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
