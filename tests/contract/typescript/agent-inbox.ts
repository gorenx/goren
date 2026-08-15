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
    throw new Error('agent inbox contract source does not match the pinned manifest')
  }

  const agentModule = await import(pathToFileURL(
    resolve(sourceRoot, 'packages/core/agent/src/index.ts'),
  ).href) as typeof import('../../../deepseek-harness/packages/core/agent/src/index.ts')
  const sessionModule = await import(pathToFileURL(
    resolve(sourceRoot, 'packages/core/session/src/index.ts'),
  ).href) as typeof import('../../../deepseek-harness/packages/core/session/src/index.ts')
  const llmModule = await import(pathToFileURL(
    resolve(sourceRoot, 'packages/llm/llm/src/index.ts'),
  ).href) as typeof import('../../../deepseek-harness/packages/llm/llm/src/index.ts')
  const { Inbox } = agentModule
  const { Session, SessionId } = sessionModule
  const { freezeMessage } = llmModule

  const notifications: Array<{ kind: string; id: string; turn?: number }> = []
  const conversation = Session.create(
    SessionId('agent-inbox-contract'), undefined,
    { version: 0, id: SessionId('agent-inbox-contract'), createdAt: 100 },
  )
  const pending = new Inbox(conversation, {
    inserted: message => { notifications.push({ kind: 'inserted', id: message.id }) },
    discarded: message => { notifications.push({ kind: 'discarded', id: message.id }) },
    claimed: (message, turn) => { notifications.push({ kind: 'claimed', id: message.id, turn }) },
  })
  const message = (id: string, text: string) => freezeMessage({
    id, role: 'user' as const, content: [{ type: 'text' as const, text }], source: { kind: 'user' as const },
  })
  const messages = {
    firstTurn: message('message-1', 'first turn'),
    laterStep: message('message-2', 'later step'),
    firstStep: message('message-3', 'first step'),
    replacement: message('message-4', 'replacement'),
    splicedTurn: message('message-5', 'spliced turn'),
    clearedTurn: message('message-6', 'cleared turn'),
    clearedStep: message('message-7', 'cleared step'),
  }
  const snapshot = () => ({
    nextTurn: pending.nextTurn.map(entry => entry.id),
    nextStep: pending.nextStep.map(entry => entry.id),
    hasPending: pending.hasPending,
  })

  pending.append('next-turn', messages.firstTurn)
  pending.append('next-step', messages.laterStep)
  pending.prepend('next-step', messages.firstStep)
  pending.replace(messages.laterStep.id, messages.replacement)
  const afterReplace = snapshot()
  const removed = pending.splice('next-turn', -1, 3, [messages.splicedTurn])
  const afterSplice = snapshot()
  const claimed = pending.claim('next-turn', 7)
  const afterClaim = snapshot()
  pending.append('next-turn', messages.clearedTurn)
  pending.append('next-step', messages.clearedStep)
  pending.clear()

  process.stdout.write(JSON.stringify({
    events: conversation.events.map(entry => ({ type: entry.type, seq: entry.seq, data: entry.data })),
    notifications,
    removed: removed.map(entry => entry.id),
    claimed: claimed.map(entry => entry.id),
    snapshots: { afterReplace, afterSplice, afterClaim, afterClear: snapshot() },
  }))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
