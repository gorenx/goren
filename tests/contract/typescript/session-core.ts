import { execFileSync } from 'node:child_process'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

type SessionEvent = {
  type: string
  seq: number
  time: number
  data: unknown
  sourceEventSeqs?: number[]
  surfaceOp?: 'append' | { op: 'replace'; start: number; end: number }
}

void (async () => {
  const sourceRoot = resolve(process.argv[2])
  const manifestPath = resolve(process.argv[3])
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8')) as {
    source: { commit: string; version: string }
  }
  const sourceCommit = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: sourceRoot, encoding: 'utf8' }).trim()
  const sourcePackage = JSON.parse(await readFile(resolve(sourceRoot, 'package.json'), 'utf8')) as { version: string }
  if (sourceCommit !== manifest.source.commit || sourcePackage.version !== manifest.source.version) {
    throw new Error('session contract source does not match the pinned manifest')
  }

  const sessionModule = await import(pathToFileURL(
    resolve(sourceRoot, 'packages/core/session/src/index.ts'),
  ).href) as typeof import('../../../deepseek-harness/packages/core/session/src/index.ts')
  const { Session, SessionId } = sessionModule
  const header = (id: string) => ({ version: 0, id: SessionId(id), createdAt: 100 })
  const observe = (value: InstanceType<typeof Session>) => ({
    header: { ...value.header, createdAt: 0 },
    firstLiveSeq: value.firstLiveSeq,
    seq: value.seq,
    events: value.events.map(event => ({ ...event, time: 0 })),
    surface: { nodes: [...value.surface.nodes], replaceGeneration: value.surface.replaceGeneration },
  })

  const blank = Session.create(SessionId('blank'), undefined, header('blank'))
  const appended = Session.create(SessionId('appended'), undefined, header('appended'))
  ;(appended as unknown as { append(type: string, data: unknown): void })
    .append('fixture/event', { items: ['value'] })

  const firstMessage = {
    id: 'message-1', role: 'user', content: [{ type: 'text', text: 'original' }], source: { kind: 'user' },
  }
  const replacementMessage = {
    id: 'message-2', role: 'user', content: [{ type: 'text', text: 'summary' }], source: { kind: 'plugin', plugin: 'fixture' },
  }
  const seed: SessionEvent[] = [
    { type: 'user/message', seq: 0, time: 1, data: firstMessage, surfaceOp: 'append' },
    {
      type: 'user/message', seq: 1, time: 2, data: replacementMessage,
      sourceEventSeqs: [0], surfaceOp: { op: 'replace', start: 0, end: 0 },
    },
  ]
  const seeded = Session.create(
    SessionId('seeded'), seed as Parameters<typeof Session.create>[1], header('seeded'),
  )
  process.stdout.write(JSON.stringify({ blank: observe(blank), appended: observe(appended), seeded: observe(seeded) }))
})().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`)
  process.exitCode = 1
})
