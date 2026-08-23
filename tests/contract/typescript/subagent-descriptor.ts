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
  const descriptorModule = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/subagent/subagent/src/descriptor.ts',
  )).href)
  const { foldSubagentDescriptor, snapshotSubagentDescriptor } = descriptorModule

  const inspect = (name: string, operation: () => unknown) => {
    try {
      const value = operation()
      return value === undefined
        ? { name, kind: 'none' }
        : { name, kind: 'value', value }
    } catch {
      return { name, kind: 'error' }
    }
  }
  const descriptorEvent = (sequence: number, data: unknown) => ({
    type: 'subagent/descriptor',
    seq: sequence,
    time: sequence,
    data,
  })
  const observations = [
    inspect('snapshot-one-shot', () => snapshotSubagentDescriptor({
      mode: 'one-shot',
      provider: 'spawn',
    })),
    inspect('snapshot-continuable', () => snapshotSubagentDescriptor({
      mode: 'continuable',
      provider: 'fork',
      label: 'review',
      agentProvider: 'deepseek',
      agentModel: 'deepseek-chat',
      persona: 'reviewer',
      toolFilter: {
        allow: [],
        deny: ['write_file'],
      },
    })),
    inspect('fold-first-authoritative', () => foldSubagentDescriptor([
      descriptorEvent(1, {
        version: 2,
        mode: 'one-shot',
        provider: 'spawn',
        label: 'first',
      }),
      descriptorEvent(2, {
        version: 2,
        mode: 'continuable',
        provider: 'fork',
        label: 'later',
      }),
    ])),
    inspect('fold-unsupported-first', () => foldSubagentDescriptor([
      descriptorEvent(1, { version: 3 }),
      descriptorEvent(2, {
        version: 2,
        mode: 'one-shot',
        provider: 'spawn',
      }),
    ])),
    inspect('fold-corrupt-current', () => foldSubagentDescriptor([
      descriptorEvent(1, {
        version: 2,
        mode: 'one-shot',
        provider: 'spawn',
        extra: true,
      }),
    ])),
    inspect('fold-empty-tool-filter', () => foldSubagentDescriptor([
      descriptorEvent(1, {
        version: 2,
        mode: 'continuable',
        provider: 'spawn',
        label: 'child',
        toolFilter: {},
      }),
    ])),
    inspect('fold-null-label', () => foldSubagentDescriptor([
      descriptorEvent(1, {
        version: 2,
        mode: 'one-shot',
        provider: 'spawn',
        label: null,
      }),
    ])),
    inspect('fold-none', () => foldSubagentDescriptor([])),
  ]
  process.stdout.write(JSON.stringify(observations))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
