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
    throw new Error('llm-deepseek contract source does not match the pinned manifest')
  }

  const [messageModule, brandModule, serializeModule, translateModule, sseModule, assemblerModule, retryModule] = await Promise.all([
    import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm/src/message.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm/src/brand.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm-deepseek/src/serialize.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm-deepseek/src/translate.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm-deepseek/src/sse.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm/src/assembler.ts')).href),
    import(pathToFileURL(resolve(sourceRoot, 'packages/llm/llm/src/retry-policy.ts')).href),
  ])
  const { createMessage } = messageModule
  const { CallId, ReasoningEffortId } = brandModule
  const { serializeMessages, serializeRequest } = serializeModule
  const { translate } = translateModule
  const { DONE } = sseModule
  const { BlockAssembler } = assemblerModule
  const { resolveRetryPolicy } = retryModule

  const conversation = [
    createMessage({
      role: 'assistant',
      content: [
        { type: 'reasoning', text: 'think' },
        { type: 'tool-call', id: CallId('call-1'), name: 'lookup', arguments: '{"q":"x"}' },
      ],
      source: { kind: 'plugin', plugin: 'contract' },
    }),
    createMessage({
      role: 'user',
      content: [
        { type: 'text', text: 'note' },
        { type: 'tool-result', toolCallId: CallId('call-1'), content: [] },
      ],
      source: { kind: 'plugin', plugin: 'contract' },
    }),
  ]
  const wireMessages = serializeMessages(conversation)
  const wireRequest = serializeRequest({
    provider: 'deepseek-official', model: 'deepseek-v4-flash', messages: conversation,
    system: 'system', reasoningEffort: ReasoningEffortId('max'), stop: [],
    tools: [{ name: 'lookup', description: 'Lookup', parameters: { type: 'object' } }],
  }, { thinking: 'enabled', reasoningEffort: 'high' })

  const payloads = [
    { choices: [{ delta: { reasoning_content: '' } }] },
    { choices: [{ delta: { reasoning_content: 'think' } }] },
    { choices: [{ delta: { content: 'answer' } }] },
    { choices: [{ delta: { tool_calls: [{ index: 0, id: 'call-2', function: { name: 'lookup', arguments: '{}' } }] } }] },
    { choices: [{ delta: {}, finish_reason: 'tool_calls' }], usage: { prompt_tokens: 10, completion_tokens: 4, prompt_cache_hit_tokens: 7 } },
    DONE,
  ]
  async function* feed(): AsyncGenerator<string> {
    for (const payload of payloads) yield typeof payload === 'string' ? payload : JSON.stringify(payload)
  }
  const chunks = []
  const assembler = new BlockAssembler()
  for await (const chunk of translate(feed())) {
    chunks.push(chunk)
    assembler.push(chunk)
  }

  process.stdout.write(JSON.stringify({
    wireMessages,
    wireRequest,
    chunks,
    assembled: { blocks: assembler.blocks(), usage: assembler.usage, finish: assembler.finish },
    retryDefault: resolveRetryPolicy(undefined, 'contract'),
  }))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
