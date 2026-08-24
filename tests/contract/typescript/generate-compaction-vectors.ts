import { execFileSync } from 'node:child_process'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const BASELINE = 'b150a551b8d465e31e418e1b2eaf5e79bbb7d28e'

void (async () => {
  const sourceRoot = resolve(process.argv[2] ?? '../deepseek-harness')
  const sourceCommit = execFileSync(
    'git',
    ['rev-parse', 'HEAD'],
    { cwd: sourceRoot, encoding: 'utf8' },
  ).trim()
  if (sourceCommit !== BASELINE) {
    throw new Error(`source commit ${sourceCommit} does not match Compaction baseline ${BASELINE}`)
  }

  const load = async <T>(relativePath: string): Promise<T> => {
    return await import(pathToFileURL(resolve(sourceRoot, relativePath)).href) as T
  }
  const sessionModule = await load<typeof import('@deepseek-ai/dsh-session')>(
    'packages/core/session/src/index.ts',
  )
  const llmModule = await load<typeof import('@deepseek-ai/dsh-llm')>(
    'packages/llm/llm/src/index.ts',
  )
  const compactionModule = await load<typeof import('@deepseek-ai/dsh-compaction')>(
    'packages/compaction/compaction/src/index.ts',
  )
  const basicConfigModule = await load<
    typeof import('@deepseek-ai/dsh-compaction-basic/src/config.ts')
  >('packages/compaction/compaction-basic/src/config.ts')
  const prunerConfigModule = await load<
    typeof import('@deepseek-ai/dsh-compaction-tool-result-pruner/src/config.ts')
  >('packages/compaction/compaction-tool-result-pruner/src/config.ts')

  const basicInputs = [
    {
      name: 'defaults',
      input: {},
      target: { provider: 'deepseek-official', model: 'deepseek-v4-pro' },
      contextWindow: 10_000,
    },
    {
      name: 'exact-model-policy',
      input: {
        thresholdRatio: 0.75,
        retainTokens: 128,
        summarizationProvider: 'summary-default',
        summarizationModel: 'summary-default-model',
        maxTokens: 2048,
        compactionRetries: 2,
        maxOverflowRetries: 3,
        auto: false,
        modelPolicies: [{
          provider: 'routed-provider',
          model: 'routed-model',
          thresholdRatio: 0.5,
          retainRatio: 0.1,
          summarizationProvider: 'summary-routed',
          summarizationModel: 'summary-routed-model',
          maxTokens: 1024,
          compactionRetries: 0,
          maxOverflowRetries: 0,
        }],
      },
      target: { provider: 'routed-provider', model: 'routed-model' },
      contextWindow: 4096,
    },
  ] as const
  const basicConfig = basicInputs.map((testCase) => {
    const resolved = basicConfigModule.resolveConfig(testCase.input)
    const targetPolicy = basicConfigModule.resolveTargetPolicy(resolved, testCase.target)
    return {
      ...testCase,
      resolved,
      compactSpec: basicConfigModule.resolveCompactSpec(
        targetPolicy,
        testCase.contextWindow,
      ),
    }
  })

  const prunerInputs = [
    { name: 'defaults', input: {} },
    {
      name: 'custom-unicode-budget',
      input: { thresholdChars: 100, headChars: 20, tailChars: 10 },
    },
  ] as const
  const prunerConfig = prunerInputs.map(testCase => ({
    ...testCase,
    resolved: prunerConfigModule.resolveConfig(testCase.input),
  }))

  const conversation = sessionModule.Session.create(
    sessionModule.SessionId('compaction-contract'),
  )
  const original = conversation.append('user/message', llmModule.createUserMessage({
    content: [{ type: 'text', text: 'history to compact' }],
    source: { kind: 'user' },
  }), { surfaceOp: 'append' })
  const compactionId = compactionModule.CompactionId('compact-fixture')
  const sourceCommandId = 'command-fixture'
  const summary = [{ type: 'text' as const, text: 'durable checkpoint' }]
  const rawOutput = [
    { type: 'reasoning' as const, text: 'private summary reasoning' },
    ...summary,
  ]
  const startEvent = conversation.append('compaction/start', {
    compactionId,
    sourceCommandId,
    turn: null,
  })
  const summaryEvent = conversation.append('compaction/summary', {
    compactionId,
    sourceCommandId,
    summary,
    rawOutput,
    llmStreamCall: true,
    shadowedRange: { start: original.seq, end: original.seq },
    shadowedSeqs: [original.seq],
    shadowedTokenCount: 9,
    provider: 'summary-provider',
    model: 'summary-model',
    maxTokens: 512,
    usage: {
      inputTokens: 40,
      outputTokens: 5,
      cacheReadTokens: 3,
      cacheWriteTokens: 2,
      reasoningTokens: 1,
    },
  })
  const checkpointEvent = conversation.append('user/message', llmModule.createUserMessage({
    content: summary,
    source: compactionModule.compactCheckpointSource(compactionId, sourceCommandId),
  }), {
    surfaceOp: { op: 'replace', start: original.seq, end: original.seq },
    sourceEventSeqs: [startEvent.seq, summaryEvent.seq, original.seq],
  })
  const endEvent = conversation.append('compaction/end', {
    compactionId,
    sourceCommandId,
    turn: null,
  })
  const pruneEvent = conversation.append('compaction/prune', {
    shadowedRange: { start: checkpointEvent.seq, end: checkpointEvent.seq },
    shadowedSeqs: [checkpointEvent.seq],
    shadowedTokenCount: 7,
  })
  const selectedEvents = [startEvent, summaryEvent, checkpointEvent, endEvent, pruneEvent]
    .map(event => ({
      type: event.type,
      seq: event.seq,
      data: event.type === 'user/message'
        ? { ...event.data, id: 'checkpoint-message-fixture' }
        : event.data,
      ...event.sourceEventSeqs === undefined
        ? {}
        : { sourceEventSeqs: event.sourceEventSeqs },
      ...event.surfaceOp === undefined ? {} : { surfaceOp: event.surfaceOp },
    }))
  const result = {
    compactionId,
    sourceCommandId,
    startSeq: startEvent.seq,
    summarySeq: summaryEvent.seq,
    endSeq: endEvent.seq,
    summary,
    shadowedRange: { start: original.seq, end: original.seq },
    shadowedSeqs: [original.seq],
    shadowedTokenCount: 9,
  }

  const rootPackage = JSON.parse(
    await readFile(resolve(sourceRoot, 'package.json'), 'utf8'),
  ) as { version: string }
  const output = {
    schemaVersion: 1,
    source: {
      commit: sourceCommit,
      version: rootPackage.version,
    },
    basicConfig,
    prunerConfig,
    pruneMarker: {
      value: prunerConfigModule.PRUNE_MARKER,
      codePoints: Array.from(prunerConfigModule.PRUNE_MARKER).length,
    },
    checkpointSource: compactionModule.compactCheckpointSource(
      compactionId,
      sourceCommandId,
    ),
    events: selectedEvents,
    result,
  }
  process.stdout.write(`${JSON.stringify(output, null, 2)}\n`)
})()
