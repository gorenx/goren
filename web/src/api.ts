import { isRecord, recordString } from './types'
import type { Translator } from './i18n'

interface ServerRequestEnvelope {
  rpcId: string
  payload: unknown
}

type FrameReceiver = (request: ServerRequestEnvelope) => void
type ConnectionReceiver = (onlineDownlinks: number) => void

interface RPCResult {
  ok: boolean
  value?: unknown
  error?: { code?: string; message?: string; details?: unknown }
}

interface RPCEnvelope {
  rpcId: string
  result: RPCResult
}

interface RPCReceipt {
  accepted: boolean
  reason?: string
}

function decodeEnvelope(value: unknown, translateText: Translator): RPCEnvelope {
  if (!isRecord(value)) throw new Error(translateText('error.invalidRPCEnvelope'))
  const rpcId = recordString(value, 'rpcId')
  const resultValue = value.result
  if (rpcId === undefined || !isRecord(resultValue) || typeof resultValue.ok !== 'boolean') {
    throw new Error(translateText('error.invalidRPCResult'))
  }
  const errorValue = resultValue.error
  const error = isRecord(errorValue) ? { message: recordString(errorValue, 'message') } : undefined
  return { rpcId, result: { ok: resultValue.ok, value: resultValue.value, error } }
}

function decodeServerRequest(value: unknown, translateText: Translator): ServerRequestEnvelope {
  if (!isRecord(value) || value.type !== 'server-request') throw new Error(translateText('error.invalidServerRequest'))
  const rpcId = recordString(value, 'rpcId')
  if (rpcId === undefined || !('payload' in value)) throw new Error(translateText('error.invalidServerRequest'))
  return { rpcId, payload: value.payload }
}

export class HarnessAPI {
  readonly #onMuxFrame: FrameReceiver
  readonly #onHostFrame: FrameReceiver
  readonly #onConnection: ConnectionReceiver
  readonly #translateText: Translator
  readonly #sockets = new Set<WebSocket>()
  readonly #online = new Set<WebSocket>()
  readonly #reconnectTimers = new Set<number>()
  #closed = false

  constructor(onMuxFrame: FrameReceiver, onHostFrame: FrameReceiver, onConnection: ConnectionReceiver, translateText: Translator) {
    this.#onMuxFrame = onMuxFrame
    this.#onHostFrame = onHostFrame
    this.#onConnection = onConnection
    this.#translateText = translateText
  }

  async call<T>(method: string, payload: object): Promise<T> {
    const rpcId = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`
    const response = await fetch(`/api/${method}`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ type: 'client-request', rpcId, method, payload }),
    })
    if (!response.ok) throw new Error(this.#translateText('error.requestFailed', { method, status: response.status }))
    const envelope = decodeEnvelope(await response.json(), this.#translateText)
    if (envelope.rpcId !== rpcId) throw new Error(this.#translateText('error.wrongRPCID', { method }))
    if (!envelope.result.ok) throw new Error(envelope.result.error?.message ?? this.#translateText('error.methodFailed', { method }))
    return envelope.result.value as T
  }

  async respond(rpcId: string, result: RPCResult): Promise<void> {
    const response = await fetch('/api/respond', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ type: 'client-response', rpcId, result }),
    })
    if (!response.ok) throw new Error(this.#translateText('error.respondFailed', { status: response.status }))
    const rawReceipt: unknown = await response.json()
    if (!isRecord(rawReceipt) || typeof rawReceipt.accepted !== 'boolean') {
      throw new Error(this.#translateText('error.invalidReceipt'))
    }
    const receipt: RPCReceipt = {
      accepted: rawReceipt.accepted,
      reason: recordString(rawReceipt, 'reason'),
    }
    if (!receipt.accepted) {
      const reason = receipt.reason === undefined ? '' : this.#translateText('error.responseReason', { reason: receipt.reason })
      throw new Error(this.#translateText('error.responseRejected', { reason }))
    }
  }

  connect(): void {
    this.#openDownlink('/api/events.mux', this.#onMuxFrame)
    this.#openDownlink('/api/events.host', this.#onHostFrame)
  }

  close(): void {
    this.#closed = true
    for (const timer of this.#reconnectTimers) globalThis.clearTimeout(timer)
    this.#reconnectTimers.clear()
    for (const socket of this.#sockets) socket.close()
    this.#sockets.clear()
    this.#online.clear()
    this.#onConnection(0)
  }

  #openDownlink(path: string, receive: FrameReceiver): void {
    if (this.#closed) return
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const socket = new WebSocket(`${protocol}//${location.host}${path}`)
    this.#sockets.add(socket)
    socket.addEventListener('open', () => {
      this.#online.add(socket)
      this.#onConnection(this.#online.size)
    })
    socket.addEventListener('message', event => {
      try {
        const envelope: unknown = JSON.parse(String(event.data))
        receive(decodeServerRequest(envelope, this.#translateText))
      } catch (error) {
        console.error(error)
      }
    })
    socket.addEventListener('close', () => {
      this.#sockets.delete(socket)
      this.#online.delete(socket)
      this.#onConnection(this.#online.size)
      if (this.#closed) return
      const timer = globalThis.setTimeout(() => {
        this.#reconnectTimers.delete(timer)
        this.#openDownlink(path, receive)
      }, 900)
      this.#reconnectTimers.add(timer)
    })
    socket.addEventListener('error', () => socket.close())
  }
}
