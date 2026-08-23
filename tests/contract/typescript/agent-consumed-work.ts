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
  const agentModule = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/core/agent/src/consumed-work.ts',
  )).href)
  const { foldConsumedWork } = agentModule

  let sequence = 0
  const event = (type: string, data: Record<string, unknown>) => ({
    type,
    seq: sequence++,
    time: sequence,
    data,
  })
  const turnStart = (turn: number) => event('turn/start', { turn })
  const stepStart = (turn: number) => event('step/start', { turn, step: 1 })
  const claim = () => event('agent/inbox/spliced', {
    target: 'next-turn',
    start: 0,
    removedCount: 1,
    inserted: [],
  })
  const cancel = (inserted: unknown[] = []) => event('agent/inbox/spliced', {
    target: 'next-turn',
    start: 0,
    removedCount: 1,
    inserted,
    outcome: 'canceled',
  })
  const turnEnd = (turn: number, reason: Record<string, unknown>) => event('turn/end', {
    turn,
    reason,
  })
  const stepped = (turn: number, reason: Record<string, unknown>) => [
    turnStart(turn),
    claim(),
    stepStart(turn),
    turnEnd(turn, reason),
  ]
  const observe = (name: string, events: unknown[]) => {
    const work = foldConsumedWork(events)
    return {
      name,
      endTurn: work.end?.data.turn ?? null,
      endKind: work.end?.data.reason.kind ?? null,
      droppedUnrun: work.droppedUnrun,
    }
  }
  const completed = { kind: 'completed' }
  const observations = [
    observe('latest-stepped', [
      ...stepped(1, completed),
      ...stepped(2, { kind: 'max-tokens' }),
    ]),
    observe('claimed-pre-step-error', [
      ...stepped(1, completed),
      turnStart(2),
      claim(),
      turnEnd(2, {
        kind: 'error',
        error: {
          code: 'MODEL',
          message: 'failed',
        },
      }),
    ]),
    observe('unclaimed-later-turns', [
      ...stepped(1, completed),
      turnStart(2),
      turnEnd(2, { kind: 'blocked' }),
    ]),
    observe('completed-empty-claim', [
      ...stepped(1, completed),
      turnStart(2),
      claim(),
      turnEnd(2, completed),
    ]),
    observe('mid-turn-suffix', [
      claim(),
      turnEnd(2, {
        kind: 'aborted',
        reason: {
          kind: 'user',
        },
      }),
    ]),
    observe('dropped-unrun', [cancel()]),
    observe('replacement-stays-pending', [cancel([{}])]),
    observe('later-turn-absorbs-drop', [
      cancel(),
      ...stepped(1, completed),
    ]),
  ]
  process.stdout.write(JSON.stringify(observations))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
