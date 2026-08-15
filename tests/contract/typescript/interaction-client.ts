import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

type Receipt = { accepted: boolean; reason?: 'not-pending' | 'bad-response' }
type ClientResponse = {
  type: 'client-response'
  rpcId: string
  result:
    | { ok: true; value?: unknown }
    | { ok: false; error: { code: string; message: string; details: unknown } }
}
type MuxEnvelope = {
  rpcId: string
  payload: { type: string; [key: string]: unknown }
}

void (async () => {
  const sourceRoot = resolve(process.argv[2])
  const baseURL = process.argv[3]
  if (baseURL === undefined) throw new Error('Go host URL is required')

  Object.defineProperty(globalThis, 'location', {
    configurable: true,
    value: { origin: baseURL },
  })
  const clientModule = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/client/connection/src/client/web-api-client.ts',
  )).href) as {
    WebApiClient: new (timeoutMs?: number) => {
      events: {
        mux(payload: object, signal: AbortSignal, onOpen: () => void): AsyncIterable<MuxEnvelope>
      }
      respond(message: ClientResponse, signal?: AbortSignal): Promise<Receipt>
    }
  }
  const approvalSchemas = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/host/apiproxy/src/api/approvals.schema.ts',
  )).href) as {
    approvalResponsePayloadSchema: { parse(value: unknown): unknown }
  }
  const questionSchemas = await import(pathToFileURL(resolve(
    sourceRoot,
    'packages/host/apiproxy/src/api/questions.schema.ts',
  )).href) as {
    questionResponsePayloadSchema: { parse(value: unknown): unknown }
  }

  const apiClient = new clientModule.WebApiClient(5_000)
  const lifecycle = new AbortController()
  let resolveOpen!: () => void
  const opened = new Promise<void>((resolveOpened) => { resolveOpen = resolveOpened })
  const muxIterator = apiClient.events.mux({}, lifecycle.signal, resolveOpen)[Symbol.asyncIterator]()
  const firstRead = muxIterator.next()
  await opened
  const buffered: MuxEnvelope[] = []
  let firstPending = true

  const nextMatching = async (wantedType: string): Promise<MuxEnvelope> => {
    const bufferedIndex = buffered.findIndex(envelope => envelope.payload.type === wantedType)
    if (bufferedIndex >= 0) return buffered.splice(bufferedIndex, 1)[0] as MuxEnvelope
    for (let index = 0; index < 200; index++) {
      let read: IteratorResult<MuxEnvelope>
      if (firstPending) {
        firstPending = false
        read = await firstRead
      } else {
        read = await Promise.race([
            muxIterator.next(),
            new Promise<never>((_resolve, reject) => {
              setTimeout(() => reject(new Error(`${wantedType} timed out`)), 5_000)
            }),
        ])
      }
      if (read.done === true) throw new Error(`${wantedType}: stream ended`)
      if (read.value.payload.type === wantedType) return read.value
      buffered.push(read.value)
    }
    throw new Error(`${wantedType}: frame budget exhausted`)
  }

  const questionRequest = await nextMatching('question/requested')
  const questionPayload = questionRequest.payload as {
    type: 'question/requested'
    sessionId: string
    questions: Array<{ id: string }>
  }
  const badQuestionReceipt = await apiClient.respond({
    type: 'client-response',
    rpcId: questionRequest.rpcId,
    result: {
      ok: true,
      value: {
        sessionId: questionPayload.sessionId,
        answer: { answers: [{ id: questionPayload.questions[0]?.id, selected: ['Unknown'] }] },
      },
    },
  })
  const questionValue = questionSchemas.questionResponsePayloadSchema.parse({
    sessionId: questionPayload.sessionId,
    answer: {
      answers: [{
        id: questionPayload.questions[0]?.id,
        selected: ['Code', 'Docs'],
        custom: 'release notes',
      }],
    },
  })
  const acceptedQuestionReceipt = await apiClient.respond({
    type: 'client-response', rpcId: questionRequest.rpcId,
    result: { ok: true, value: questionValue },
  })
  const questionResolved = await nextMatching('question/resolved')
  const duplicateQuestionReceipt = await apiClient.respond({
    type: 'client-response', rpcId: questionRequest.rpcId,
    result: { ok: true, value: questionValue },
  })

  const approvalRequest = await nextMatching('approval/requested')
  const approvalPayload = approvalRequest.payload as {
    type: 'approval/requested'
    sessionId: string
    approvalId: string
  }
  const badApprovalReceipt = await apiClient.respond({
    type: 'client-response',
    rpcId: approvalRequest.rpcId,
    result: {
      ok: true,
      value: {
        sessionId: approvalPayload.sessionId,
        approvalId: 'wrong-approval',
        outcome: 'allowed-once',
      },
    },
  })
  const approvalValue = approvalSchemas.approvalResponsePayloadSchema.parse({
    sessionId: approvalPayload.sessionId,
    approvalId: approvalPayload.approvalId,
    outcome: 'allowed-once',
  })
  const acceptedApprovalReceipt = await apiClient.respond({
    type: 'client-response', rpcId: approvalRequest.rpcId,
    result: { ok: true, value: approvalValue },
  })
  const approvalResolved = await nextMatching('approval/resolved')
  const duplicateApprovalReceipt = await apiClient.respond({
    type: 'client-response', rpcId: approvalRequest.rpcId,
    result: { ok: true, value: approvalValue },
  })

  lifecycle.abort()
  await Promise.allSettled([muxIterator.return?.()])
  process.stdout.write(`${JSON.stringify({
    approval: {
      badReceipt: badApprovalReceipt,
      acceptedReceipt: acceptedApprovalReceipt,
      duplicateReceipt: duplicateApprovalReceipt,
      resolvedOutcome: approvalResolved.payload.outcome,
      correlationKept: approvalResolved.payload.approvalId === approvalPayload.approvalId,
    },
    question: {
      badReceipt: badQuestionReceipt,
      acceptedReceipt: acceptedQuestionReceipt,
      duplicateReceipt: duplicateQuestionReceipt,
      resolvedOutcome: questionResolved.payload.outcome,
      correlationKept: questionResolved.payload.questionRpcId === questionRequest.rpcId,
    },
  })}\n`)
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
