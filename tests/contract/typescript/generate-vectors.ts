import { execFileSync } from 'node:child_process'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

type Parser = { safeParse(value: unknown): { success: boolean; data?: unknown } }
type Candidate = { name: string; input: unknown }
type ContractVector = { name: string; accepted: boolean; input: unknown; normalized?: unknown }

type Manifest = {
  schemaVersion: number
  source: { commit: string; version: string }
  included: { privilegedMethods: string[] }
}

void (async () => {
const scriptDirectory = dirname(resolve(process.argv[1]))
const repositoryRoot = resolve(scriptDirectory, '../../..')
const sourceRoot = resolve(process.argv[2] ?? resolve(repositoryRoot, '../deepseek-harness'))
const manifestPath = resolve(process.argv[3] ?? resolve(repositoryRoot, 'contracts/deepseek-harness/manifest.json'))
const manifest = JSON.parse(await readFile(manifestPath, 'utf8')) as Manifest

const sourceCommit = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: sourceRoot, encoding: 'utf8' }).trim()
const sourcePackage = JSON.parse(await readFile(resolve(sourceRoot, 'package.json'), 'utf8')) as { version: string }
if (sourceCommit !== manifest.source.commit) {
  throw new Error(`source commit ${sourceCommit} does not match manifest ${manifest.source.commit}`)
}
if (sourcePackage.version !== manifest.source.version) {
  throw new Error(`source version ${sourcePackage.version} does not match manifest ${manifest.source.version}`)
}
const connectionSource = await readFile(resolve(sourceRoot, 'packages/client/connection/src/index.ts'), 'utf8')
const privilegedBlock = /const PRIVILEGED_METHODS = new Set\(\[([\s\S]*?)\]\)/.exec(connectionSource)?.[1]
if (privilegedBlock === undefined) throw new Error('source PRIVILEGED_METHODS declaration was not found')
const sourcePrivilegedMethods = [...privilegedBlock.matchAll(/^\s*'([^']+)',?\s*$/gm)].map(match => match[1])
if (JSON.stringify(sourcePrivilegedMethods) !== JSON.stringify(manifest.included.privilegedMethods)) {
  throw new Error('source PRIVILEGED_METHODS does not match the manifest')
}

const load = async <T>(relativePath: string): Promise<T> => {
  return await import(pathToFileURL(resolve(sourceRoot, relativePath)).href) as T
}

const rpcSchemas = await load<{
  clientRequestSchema: Parser
  clientResponseSchema: Parser
  serverRequestSchema: Parser
  serverResponseSchema: Parser
  rpcReceiptSchema: Parser
}>('packages/host/apiproxy/src/api/rpc.schema.ts')
const eventSchemas = await load<{ muxFrameSchema: Parser; hostFrameSchema: Parser }>(
  'packages/host/apiproxy/src/api/events.schema.ts',
)
const hostSchemas = await load<{ hostDescribeRequestSchema: Parser; hostDescribeValueSchema: Parser }>(
  'packages/host/apiproxy/src/api/host.schema.ts',
)
const sessionSchemas = await load<{
  sessionListRequestSchema: Parser
  sessionListValueSchema: Parser
  sessionCreateRequestSchema: Parser
  sessionCreateValueSchema: Parser
  sessionHistoryRequestSchema: Parser
  sessionHistoryValueSchema: Parser
  sessionModelsRequestSchema: Parser
  sessionModelsValueSchema: Parser
  sessionSelectModelRequestSchema: Parser
  sessionSelectModelValueSchema: Parser
  sessionPromptRequestSchema: Parser
  sessionPromptValueSchema: Parser
  sessionUpdateQueueRequestSchema: Parser
  sessionUpdateQueueValueSchema: Parser
  sessionCancelRequestSchema: Parser
  sessionCancelValueSchema: Parser
}>('packages/host/apiproxy/src/api/sessions.schema.ts')

const internalError = { code: 'internal', message: 'boom', details: {} }
const workspace = {
  workspaceId: 'workspace-1', path: '/workspace', title: 'Workspace', sessionIds: [],
  createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z',
}

const candidates: Record<string, { schema: Parser; values: Candidate[] }> = {
  clientRequest: {
    schema: rpcSchemas.clientRequestSchema,
    values: [
      { name: 'canonical', input: { type: 'client-request', rpcId: 'client-1', method: 'host.describe', payload: {} } },
      { name: 'unknown-field-stripped', input: { type: 'client-request', rpcId: 'client-2', method: 'host.describe', payload: {}, extra: true } },
      { name: 'missing-payload', input: { type: 'client-request', rpcId: 'client-3', method: 'host.describe' } },
      { name: 'wrong-discriminant', input: { type: 'server-request', rpcId: 'client-4', method: 'host.describe', payload: {} } },
    ],
  },
  clientResponse: {
    schema: rpcSchemas.clientResponseSchema,
    values: [
      { name: 'void-success', input: { type: 'client-response', rpcId: 'server-1', result: { ok: true } } },
      { name: 'business-failure', input: { type: 'client-response', rpcId: 'server-2', result: { ok: false, error: internalError } } },
      { name: 'missing-error-details', input: { type: 'client-response', rpcId: 'server-3', result: { ok: false, error: { code: 'internal', message: 'boom' } } } },
    ],
  },
  serverRequest: {
    schema: rpcSchemas.serverRequestSchema,
    values: [
      { name: 'canonical', input: { type: 'server-request', rpcId: 'stream-1', method: 'session/subscribed', payload: { type: 'session/subscribed', sessionId: 'session-1', lastSeq: -1 } } },
      { name: 'missing-method', input: { type: 'server-request', rpcId: 'stream-2', payload: {} } },
    ],
  },
  serverResponse: {
    schema: rpcSchemas.serverResponseSchema,
    values: [
      { name: 'host-describe-success', input: { type: 'server-response', rpcId: 'client-1', result: { ok: true, value: { version: '0.1.0-rc.5', cwd: '/workspace', attachedSessions: 0, canOpenPath: false } } } },
      { name: 'missing-result', input: { type: 'server-response', rpcId: 'client-1' } },
    ],
  },
  receipt: {
    schema: rpcSchemas.rpcReceiptSchema,
    values: [
      { name: 'accepted', input: { accepted: true } },
      { name: 'not-pending', input: { accepted: false, reason: 'not-pending' } },
      { name: 'bad-response', input: { accepted: false, reason: 'bad-response' } },
      { name: 'unknown-reason', input: { accepted: false, reason: 'unknown' } },
    ],
  },
  hostDescribeRequest: {
    schema: hostSchemas.hostDescribeRequestSchema,
    values: [
      { name: 'empty', input: {} },
      { name: 'unknown-field-stripped', input: { ignored: true } },
      { name: 'not-an-object', input: null },
    ],
  },
  hostDescribeValue: {
    schema: hostSchemas.hostDescribeValueSchema,
    values: [
      { name: 'minimal', input: { version: '0.1.0-rc.5', cwd: '/workspace', attachedSessions: 0, canOpenPath: false } },
      { name: 'with-model', input: { version: '0.1.0-rc.5', cwd: '/workspace', provider: 'deepseek', model: 'deepseek-chat', attachedSessions: 2, canOpenPath: true } },
      { name: 'negative-sessions', input: { version: '0.1.0-rc.5', cwd: '/workspace', attachedSessions: -1, canOpenPath: false } },
    ],
  },
  sessionListRequest: {
    schema: sessionSchemas.sessionListRequestSchema,
    values: [
      { name: 'empty', input: {} },
      { name: 'cursor-and-unknown', input: { cursor: 'reserved', ignored: true } },
      { name: 'null-cursor', input: { cursor: null } },
    ],
  },
  sessionListValue: {
    schema: sessionSchemas.sessionListValueSchema,
    values: [
      { name: 'empty', input: { items: [] } },
      { name: 'attached', input: { items: [{ sessionId: 'session-1', updatedAt: 7, running: false, blank: true, cwd: '/workspace' }] } },
      { name: 'missing-items', input: {} },
    ],
  },
  sessionCreateRequest: {
    schema: sessionSchemas.sessionCreateRequestSchema,
    values: [
      { name: 'default', input: {} },
      { name: 'explicit', input: { cwd: '/workspace', sessionId: 'session-1', agentPreset: 'coding', ignored: true } },
      { name: 'workspace', input: { workspaceId: 'workspace-1' } },
      { name: 'workspace-and-cwd', input: { workspaceId: 'workspace-1', cwd: '/workspace' } },
      { name: 'null-session', input: { sessionId: null } },
    ],
  },
  sessionCreateValue: {
    schema: sessionSchemas.sessionCreateValueSchema,
    values: [
      { name: 'minimal', input: { sessionId: 'session-1' } },
      { name: 'preset', input: { sessionId: 'session-1', agentPreset: 'coding' } },
      { name: 'empty-id', input: { sessionId: '' } },
    ],
  },
  sessionHistoryRequest: {
    schema: sessionSchemas.sessionHistoryRequestSchema,
    values: [
      { name: 'tail', input: { sessionId: 'session-1' } },
      { name: 'page', input: { sessionId: 'session-1', beforeSeq: 10, maxMessages: 50, ignored: true } },
      { name: 'negative-before', input: { sessionId: 'session-1', beforeSeq: -1 } },
      { name: 'zero-max', input: { sessionId: 'session-1', maxMessages: 0 } },
      { name: 'null-before', input: { sessionId: 'session-1', beforeSeq: null } },
    ],
  },
  sessionHistoryValue: {
    schema: sessionSchemas.sessionHistoryValueSchema,
    values: [
      { name: 'empty', input: { events: [], hasMore: false } },
      { name: 'event', input: { events: [{ event: { type: 'turn/start', seq: 0, time: 1, data: { turn: 1 } } }], hasMore: false } },
      { name: 'missing-more', input: { events: [] } },
    ],
  },
  sessionModelsRequest: {
    schema: sessionSchemas.sessionModelsRequestSchema,
    values: [
      { name: 'canonical', input: { sessionId: 'session-1', ignored: true } },
      { name: 'empty-id', input: { sessionId: '' } },
      { name: 'null-id', input: { sessionId: null } },
    ],
  },
  sessionModelsValue: {
    schema: sessionSchemas.sessionModelsValueSchema,
    values: [
      { name: 'empty-directory', input: { current: { provider: 'p', model: 'm' }, routable: true, groups: [], failures: [] } },
      { name: 'group', input: { current: { provider: 'p', model: 'm', reasoningEffort: 'high' }, routable: true, groups: [{ id: 'p', name: 'Provider', models: [{ id: 'm', name: 'Model' }] }], failures: [] } },
      { name: 'missing-failures', input: { current: { provider: 'p', model: 'm' }, routable: true, groups: [] } },
    ],
  },
  sessionSelectModelRequest: {
    schema: sessionSchemas.sessionSelectModelRequestSchema,
    values: [
      { name: 'canonical', input: { sessionId: 'session-1', provider: 'p', model: 'm', reasoningEffort: 'high', ignored: true } },
      { name: 'minimal', input: { sessionId: 'session-1', provider: 'p', model: 'm' } },
      { name: 'empty-provider', input: { sessionId: 'session-1', provider: '', model: 'm' } },
      { name: 'null-effort', input: { sessionId: 'session-1', provider: 'p', model: 'm', reasoningEffort: null } },
    ],
  },
  sessionSelectModelValue: {
    schema: sessionSchemas.sessionSelectModelValueSchema,
    values: [
      { name: 'canonical', input: { selected: { provider: 'p', model: 'm' } } },
      { name: 'missing-selected', input: {} },
    ],
  },
  sessionPromptRequest: {
    schema: sessionSchemas.sessionPromptRequestSchema,
    values: [
      { name: 'text', input: { sessionId: 'session-1', mode: 'queue', content: [{ type: 'text', text: 'hello', ignored: true }], clientTimeZone: 'UTC', ignored: true } },
      { name: 'image', input: { sessionId: 'session-1', mode: 'steer', content: [{ type: 'image', mediaType: 'image/png', data: 'AA==' }] } },
      { name: 'unknown-mode', input: { sessionId: 'session-1', mode: 'later', content: [] } },
      { name: 'unknown-part', input: { sessionId: 'session-1', mode: 'queue', content: [{ type: 'audio', data: '' }] } },
      { name: 'null-zone', input: { sessionId: 'session-1', mode: 'queue', content: [], clientTimeZone: null } },
    ],
  },
  sessionPromptValue: {
    schema: sessionSchemas.sessionPromptValueSchema,
    values: [
      { name: 'accepted', input: { accepted: true } },
      { name: 'false', input: { accepted: false } },
    ],
  },
  sessionUpdateQueueRequest: {
    schema: sessionSchemas.sessionUpdateQueueRequestSchema,
    values: [
      { name: 'edit', input: { sessionId: 'session-1', itemId: 'message-1', action: { kind: 'edit', content: [{ type: 'text', text: 'changed', extension: 1 }] } } },
      { name: 'remove', input: { sessionId: 'session-1', itemId: 'message-1', action: { kind: 'remove', ignored: true } } },
      { name: 'steer', input: { sessionId: 'session-1', itemId: 'message-1', action: { kind: 'steer' } } },
      { name: 'unknown-action', input: { sessionId: 'session-1', itemId: 'message-1', action: { kind: 'later' } } },
      { name: 'null-content', input: { sessionId: 'session-1', itemId: 'message-1', action: { kind: 'edit', content: null } } },
    ],
  },
  sessionUpdateQueueValue: {
    schema: sessionSchemas.sessionUpdateQueueValueSchema,
    values: [
      { name: 'accepted', input: { accepted: true } },
      { name: 'false', input: { accepted: false } },
    ],
  },
  sessionCancelRequest: {
    schema: sessionSchemas.sessionCancelRequestSchema,
    values: [
      { name: 'canonical', input: { sessionId: 'session-1', ignored: true } },
      { name: 'empty-id', input: { sessionId: '' } },
      { name: 'missing-id', input: {} },
    ],
  },
  sessionCancelValue: {
    schema: sessionSchemas.sessionCancelValueSchema,
    values: [
      { name: 'accepted', input: { accepted: true } },
      { name: 'false', input: { accepted: false } },
    ],
  },
  muxFrame: {
    schema: eventSchemas.muxFrameSchema,
    values: [
      { name: 'session/event', input: { type: 'session/event', sessionId: 'session-1', event: { type: 'turn/start', seq: 0, time: 1, data: { turn: 1 } } } },
      { name: 'session/subscribed', input: { type: 'session/subscribed', sessionId: 'session-1', lastSeq: -1 } },
      { name: 'approval/requested', input: { type: 'approval/requested', sessionId: 'session-1', approvalId: 'approval-1', toolName: 'bash' } },
      { name: 'approval/resolved', input: { type: 'approval/resolved', sessionId: 'session-1', approvalId: 'approval-1', outcome: 'allowed-once' } },
      { name: 'question/requested', input: { type: 'question/requested', sessionId: 'session-1', questions: [{ id: 'question-1', question: 'Continue?' }] } },
      { name: 'question/resolved', input: { type: 'question/resolved', sessionId: 'session-1', questionRpcId: 'question-rpc-1', outcome: 'answered' } },
      { name: 'session/queue', input: { type: 'session/queue', sessionId: 'session-1', items: [] } },
      { name: 'session/jobs', input: { type: 'session/jobs', sessionId: 'session-1', jobs: [] } },
      { name: 'session/projection', input: { type: 'session/projection', sessionId: 'session-1', key: 'todos', value: null, seq: 0 } },
      { name: 'stream/error', input: { type: 'stream/error', error: internalError } },
      { name: 'empty-question-batch', input: { type: 'question/requested', sessionId: 'session-1', questions: [] } },
      { name: 'unknown-approval-outcome', input: { type: 'approval/resolved', sessionId: 'session-1', approvalId: 'approval-1', outcome: 'maybe' } },
    ],
  },
  hostFrame: {
    schema: eventSchemas.hostFrameSchema,
    values: [
      { name: 'host/session-added', input: { type: 'host/session-added', sessionId: 'session-1', blank: true } },
      { name: 'host/session-removed', input: { type: 'host/session-removed', sessionId: 'session-1' } },
      { name: 'host/session-status', input: { type: 'host/session-status', sessionId: 'session-1', running: true } },
      { name: 'host/agent-error', input: { type: 'host/agent-error', sessionId: 'session-1', message: 'boom' } },
      { name: 'host/workspace-changed', input: { type: 'host/workspace-changed', workspace } },
      { name: 'host/workspace-removed', input: { type: 'host/workspace-removed', workspaceId: 'workspace-1' } },
      { name: 'host/workspace-order-changed', input: { type: 'host/workspace-order-changed', workspaceIds: [] } },
      { name: 'host/archived-sessions-changed', input: { type: 'host/archived-sessions-changed', archivedSessionIds: [] } },
      { name: 'host/remote-event', input: { type: 'host/remote-event', event: 'commands/change', args: [] } },
      { name: 'stream/error', input: { type: 'stream/error', error: internalError } },
      { name: 'unknown-origin', input: { type: 'host/session-added', sessionId: 'session-1', blank: true, origin: 'user' } },
      { name: 'missing-workspace-order', input: { type: 'host/workspace-order-changed' } },
    ],
  },
}

const parseVectors = (schema: Parser, values: Candidate[]): ContractVector[] => values.map(({ name, input }) => {
  const parsed = schema.safeParse(input)
  return parsed.success
    ? { name, accepted: true, input, normalized: parsed.data }
    : { name, accepted: false, input }
})

const suites = Object.fromEntries(
  Object.entries(candidates).map(([name, suite]) => [name, parseVectors(suite.schema, suite.values)]),
)
const output = {
  schemaVersion: manifest.schemaVersion,
  source: { commit: sourceCommit, version: sourcePackage.version },
  suites,
}
process.stdout.write(`${JSON.stringify(output, null, 2)}\n`)
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
