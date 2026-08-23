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
  const outputModule = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/subagent/subagent/src/assistant-output.ts',
  )).href)
  const { finalAssistantOutput } = outputModule

  const message = (content: unknown[]) => ({
    type: 'assistant/message',
    data: {
      message: {
        content,
      },
    },
  })
  const chunk = (type: string, text: string) => ({
    type: 'assistant/chunk',
    data: {
      chunk: {
        type,
        text,
      },
    },
  })
  const observe = (name: string, events: unknown[]) => ({
    name,
    output: finalAssistantOutput(events) ?? null,
  })
  const observations = [
    observe('last-non-empty-message', [
      message([{ type: 'text', text: 'step one' }]),
      message([{ type: 'text', text: 'step two' }]),
      message([]),
    ]),
    observe('message-over-stream', [
      chunk('text-delta', 'earlier partial'),
      message([{ type: 'text', text: 'complete answer' }]),
      chunk('text-delta', 'later partial'),
      message([]),
    ]),
    observe('reasoning-message', [
      chunk('text-delta', 'streamed text'),
      message([{ type: 'reasoning', text: 'complete reasoning' }]),
    ]),
    observe('text-fallback', [
      chunk('reasoning-delta', 'thinking'),
      chunk('text-delta', 'partial '),
      { type: 'tool/result', data: {} },
      chunk('text-delta', 'answer'),
      message([]),
    ]),
    observe('no-output', [
      chunk('reasoning-delta', 'thinking'),
      message([]),
    ]),
  ]
  process.stdout.write(JSON.stringify(observations))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
