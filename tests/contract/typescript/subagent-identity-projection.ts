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
  const projectionModule = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/subagent/subagent/src/projection.ts',
  )).href)
  const { subagentIdentityProjectionDefinition: definition } = projectionModule
  const descriptor = (seq: number, time: number, data: unknown) => ({
    type: 'subagent/descriptor',
    seq,
    time,
    data,
  })
  const fold = (events: unknown[]) => {
    let state = definition.init()
    for (const entry of events) {
      state = definition.apply(state, entry as Parameters<typeof definition.apply>[1])
    }
    return definition.wire.view(state)
  }
  const observations = [
    {
      name: 'last-wins',
      value: fold([
        descriptor(2, 100, {
          version: 2,
          mode: 'one-shot',
          provider: 'spawn',
          label: 'ancestor',
        }),
        descriptor(8, 200, {
          version: 2,
          mode: 'continuable',
          provider: 'fork',
          label: 'child',
        }),
      ]),
    },
    {
      name: 'damage-resets',
      value: fold([
        descriptor(2, 100, {
          version: 2,
          mode: 'one-shot',
          provider: 'spawn',
        }),
        descriptor(9, 300, {
          version: 2,
          mode: 'continuable',
        }),
      ]),
    },
    {
      name: 'unsupported-resets',
      value: fold([
        descriptor(2, 100, {
          version: 2,
          mode: 'one-shot',
          provider: 'spawn',
        }),
        descriptor(9, 300, { version: 3 }),
      ]),
    },
  ]
  process.stdout.write(JSON.stringify(observations))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
