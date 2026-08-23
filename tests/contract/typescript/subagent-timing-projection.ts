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
  const { subagentTimingProjectionDefinition: definition } = projectionModule
  const event = (type: string, seq: number, time: number) => ({
    type,
    seq,
    time,
    data: {},
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
      name: 'reset-and-active',
      value: fold([
        event('turn/start', 0, 100),
        event('subagent/descriptor', 1, 110),
        event('turn/end', 2, 300),
        event('turn/start', 3, 1_000),
        event('subagent/descriptor', 4, 1_100),
        event('turn/end', 5, 4_100),
        event('turn/start', 6, 10_000),
        event('assistant/chunk', 7, 10_500),
      ]),
    },
    {
      name: 'closed-seed',
      value: fold([
        event('turn/start', 0, 100),
        event('turn/end', 1, 200),
        event('subagent/descriptor', 2, 300),
      ]),
    },
    {
      name: 'clamps-negative-duration',
      value: fold([
        event('subagent/descriptor', 0, 100),
        event('turn/start', 1, 500),
        event('turn/end', 2, 400),
      ]),
    },
  ]
  process.stdout.write(JSON.stringify(observations))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
