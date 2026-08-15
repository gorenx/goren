import { stat } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { createFrameMatcher } from './stream-matcher.ts'

type Result<T> = { rpcId: string; result: { ok: true; value: T } | { ok: false; error: { code: string } } }
type WorkspaceView = {
  workspaceId: string
  path: string
  title: string
  sessionIds: string[]
  createdAt: string
  updatedAt: string
}
type Frame = { rpcId: string; payload: { type: string; [key: string]: unknown } }

let stopHostStream: (() => Promise<void>) | undefined

void (async () => {
  const sourceRoot = resolve(process.argv[2])
  const baseURL = process.argv[3]
  const firstPath = process.argv[4]
  const secondPath = process.argv[5]
  if (baseURL === undefined || firstPath === undefined || secondPath === undefined) {
    throw new Error('Go host URL and two Workspace paths are required')
  }

  Object.defineProperty(globalThis, 'location', {
    configurable: true,
    value: { origin: baseURL },
  })
  const moduleURL = pathToFileURL(resolve(
    sourceRoot,
    'packages/client/connection/src/client/web-api-client.ts',
  )).href
  const { WebApiClient } = await import(moduleURL) as {
    WebApiClient: new (timeoutMs?: number) => {
      workspace: {
        list(payload: object): Promise<Result<{ items: WorkspaceView[]; archivedSessionIds: string[] }>>
        create(payload: object): Promise<Result<{ workspace: WorkspaceView; created: boolean }>>
        rename(payload: object): Promise<Result<{ workspace: WorkspaceView }>>
        delete(payload: object): Promise<Result<{ deleted: true }>>
        insertBefore(payload: object): Promise<Result<{ workspaceIds: string[] }>>
        insertSessionBefore(payload: object): Promise<Result<{ workspace: WorkspaceView }>>
        archiveSession(payload: object): Promise<Result<{ archivedSessionIds: string[] }>>
      }
      sessions: {
        create(payload: object): Promise<Result<{ sessionId: string }>>
        list(payload: object): Promise<Result<{ items: Array<{ sessionId: string; cwd?: string }> }>>
      }
      events: {
        host(payload: object, signal: AbortSignal, onOpen: () => void): AsyncIterable<Frame>
      }
    }
  }

  const unwrap = <T>(response: Result<T>): T => {
    if (!response.result.ok) throw new Error(`RPC failed: ${JSON.stringify(response.result.error)}`)
    return response.result.value
  }
  const apiClient = new WebApiClient(5_000)
  const lifecycle = new AbortController()
  let resolveHostOpen!: () => void
  const hostOpen = new Promise<void>(resolveOpen => { resolveHostOpen = resolveOpen })
  const hostIterator = apiClient.events.host({}, lifecycle.signal, resolveHostOpen)[Symbol.asyncIterator]()
  stopHostStream = async () => {
    lifecycle.abort()
    await Promise.allSettled([hostIterator.return?.()])
  }
  const firstHost = hostIterator.next()
  await hostOpen
  const nextHost = createFrameMatcher(hostIterator, { timeoutMs: 5_000, maxFrames: 100 })

  const initial = unwrap(await apiClient.workspace.list({}))
  const first = unwrap(await apiClient.workspace.create({ path: firstPath }))
  const firstChanged = await firstHost
  if (firstChanged.done === true || firstChanged.value.payload.type !== 'host/workspace-changed') {
    throw new Error(`unexpected first Workspace frame: ${JSON.stringify(firstChanged)}`)
  }
  const repeated = unwrap(await apiClient.workspace.create({ path: `${firstPath}/../${firstPath.split('/').pop()}` }))
  const renamed = unwrap(await apiClient.workspace.rename({
    workspaceId: first.workspace.workspaceId,
    title: '  Primary  ',
  }))
  const renameFrame = await nextHost(
    frame => frame.payload.type === 'host/workspace-changed'
      && (frame.payload.workspace as WorkspaceView).title === 'Primary',
    'Workspace rename frame',
  )

  const firstSession = unwrap(await apiClient.sessions.create({
    workspaceId: first.workspace.workspaceId,
    sessionId: 'workspace-session-1',
  }))
  await nextHost(
    frame => frame.payload.type === 'host/workspace-changed'
      && (frame.payload.workspace as WorkspaceView).sessionIds.includes(firstSession.sessionId),
    'first Session accounting frame',
  )
  const combinedWorkspaceAndCwd = await apiClient.sessions.create({
    workspaceId: first.workspace.workspaceId,
    sessionId: 'workspace-session-invalid',
    cwd: secondPath,
  })
  const secondSession = unwrap(await apiClient.sessions.create({
    workspaceId: first.workspace.workspaceId,
    sessionId: 'workspace-session-2',
  }))
  await nextHost(
    frame => frame.payload.type === 'host/workspace-changed'
      && (frame.payload.workspace as WorkspaceView).sessionIds.includes(secondSession.sessionId),
    'second Session accounting frame',
  )
  const movedSessions = unwrap(await apiClient.workspace.insertSessionBefore({
    workspaceId: first.workspace.workspaceId,
    sessionId: firstSession.sessionId,
    beforeSessionId: secondSession.sessionId,
  }))

  const second = unwrap(await apiClient.workspace.create({ path: secondPath }))
  await nextHost(
    frame => frame.payload.type === 'host/workspace-changed'
      && (frame.payload.workspace as WorkspaceView).workspaceId === second.workspace.workspaceId,
    'second Workspace frame',
  )
  const nameConflict = await apiClient.workspace.rename({
    workspaceId: second.workspace.workspaceId,
    title: 'Primary',
  })
  const reordered = unwrap(await apiClient.workspace.insertBefore({
    workspaceId: first.workspace.workspaceId,
    beforeWorkspaceId: second.workspace.workspaceId,
  }))
  const orderFrame = await nextHost(
    frame => frame.payload.type === 'host/workspace-order-changed',
    'Workspace order frame',
  )
  const archived = unwrap(await apiClient.workspace.archiveSession({ sessionId: firstSession.sessionId }))
  const archiveFrame = await nextHost(
    frame => frame.payload.type === 'host/archived-sessions-changed',
    'archive frame',
  )
  const deleted = unwrap(await apiClient.workspace.delete({ workspaceId: second.workspace.workspaceId }))
  const removedFrame = await nextHost(
    frame => frame.payload.type === 'host/workspace-removed'
      && frame.payload.workspaceId === second.workspace.workspaceId,
    'Workspace removed frame',
  )
  const sessionList = unwrap(await apiClient.sessions.list({}))
  const archivedAgain = unwrap(await apiClient.workspace.archiveSession({ sessionId: firstSession.sessionId }))
  const missingRename = await apiClient.workspace.rename({
    workspaceId: second.workspace.workspaceId,
    title: 'Missing',
  })
  const missingDelete = await apiClient.workspace.delete({ workspaceId: second.workspace.workspaceId })
  const missingOrder = await apiClient.workspace.insertBefore({ workspaceId: 'missing-workspace' })
  const missingMoveWorkspace = await apiClient.workspace.insertSessionBefore({
    workspaceId: 'missing-workspace',
    sessionId: firstSession.sessionId,
  })
  const invalidMove = await apiClient.workspace.insertSessionBefore({
    workspaceId: first.workspace.workspaceId,
    sessionId: 'missing-session',
  })
  const unknownArchive = await apiClient.workspace.archiveSession({ sessionId: 'missing-session' })
  const finalList = unwrap(await apiClient.workspace.list({}))
  const invalidPath = await apiClient.workspace.create({ path: resolve(firstPath, 'missing') })
  const secondDirectoryRemains = (await stat(secondPath)).isDirectory()

  await stopHostStream()
  stopHostStream = undefined
  process.stdout.write(`${JSON.stringify({
    initialEmpty: initial.items.length === 0 && initial.archivedSessionIds.length === 0,
    created: first.created,
    repeated: !repeated.created && repeated.workspace.workspaceId === first.workspace.workspaceId,
    renamed: renamed.workspace.title === 'Primary',
    renameFrame: (renameFrame.payload.workspace as WorkspaceView).title === 'Primary',
    attached: movedSessions.workspace.sessionIds.join(',') === 'workspace-session-1,workspace-session-2',
    nameConflict: !nameConflict.result.ok && nameConflict.result.error.code === 'workspace-name-conflict',
    reordered: reordered.workspaceIds.join(',') === `${first.workspace.workspaceId},${second.workspace.workspaceId}`,
    orderFrame: (orderFrame.payload.workspaceIds as string[]).join(',') === reordered.workspaceIds.join(','),
    archived: archived.archivedSessionIds.join(',') === firstSession.sessionId,
    archiveFrame: (archiveFrame.payload.archivedSessionIds as string[]).join(',') === firstSession.sessionId,
    archiveIdempotent: archivedAgain.archivedSessionIds.join(',') === firstSession.sessionId,
    deleted: deleted.deleted && removedFrame.payload.workspaceId === second.workspace.workspaceId,
    registrationOnlyDelete: secondDirectoryRemains,
    notFoundErrors: [missingRename, missingDelete, missingOrder, missingMoveWorkspace]
      .every(response => !response.result.ok && response.result.error.code === 'workspace-not-found'),
    moveInvalid: !invalidMove.result.ok && invalidMove.result.error.code === 'workspace-move-invalid',
    unknownSession: !unknownArchive.result.ok && unknownArchive.result.error.code === 'session-not-found',
    workspaceAndCwdRejected: !combinedWorkspaceAndCwd.result.ok
      && combinedWorkspaceAndCwd.result.error.code === 'bad-request',
    workspaceCwd: sessionList.items.find(item => item.sessionId === secondSession.sessionId)?.cwd === first.workspace.path,
    finalList: finalList.items.length === 1
      && finalList.items[0]?.workspaceId === first.workspace.workspaceId
      && finalList.archivedSessionIds[0] === firstSession.sessionId
      && finalList.items[0]?.sessionIds.includes(firstSession.sessionId),
    invalidPath: !invalidPath.result.ok && invalidPath.result.error.code === 'workspace-invalid-path',
  })}\n`)
})().catch(async (error: unknown) => {
  await stopHostStream?.()
  console.error(error)
  process.exitCode = 1
})
