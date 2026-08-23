import { afterEach, describe, expect, it, vi } from 'vitest'
import { HarnessAPI } from './api'
import type { Translator } from './i18n'

const translate: Translator = (messageKey, values) => {
  const suffix = values === undefined ? '' : ` ${JSON.stringify(values)}`
  return `${messageKey}${suffix}`
}

describe('HarnessAPI unary RPC', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('correlates a client request with the Host result', async () => {
    let requestBody: unknown
    vi.stubGlobal('fetch', vi.fn(async (_input: string | URL | Request, init?: RequestInit) => {
      requestBody = JSON.parse(String(init?.body))
      const rpcId = readRPCID(requestBody)
      return jsonResponse({
        rpcId,
        result: {
          ok: true,
          value: {
            version: 'test',
          },
        },
      })
    }))
    const api = createAPI()

    await expect(api.call('host.describe', {})).resolves.toEqual({ version: 'test' })
    expect(requestBody).toMatchObject({
      type: 'client-request',
      method: 'host.describe',
      payload: {},
    })
  })

  it('rejects a Host result correlated to another request', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({
      rpcId: 'another-request',
      result: {
        ok: true,
        value: {},
      },
    })))
    const api = createAPI()

    await expect(api.call('session.list', {})).rejects.toThrow('error.wrongRPCID')
  })

  it('sends a correlated client response for a pending Host request', async () => {
    let requestBody: unknown
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      expect(String(input)).toBe('/api/respond')
      requestBody = JSON.parse(String(init?.body))
      return jsonResponse({ accepted: true })
    }))
    const api = createAPI()

    await api.respond('question-1', {
      ok: true,
      value: {
        answer: {
          answers: [{ id: 'choice', selected: ['A'] }],
        },
      },
    })

    expect(requestBody).toEqual({
      type: 'client-response',
      rpcId: 'question-1',
      result: {
        ok: true,
        value: {
          answer: {
            answers: [{ id: 'choice', selected: ['A'] }],
          },
        },
      },
    })
  })
})

function createAPI(): HarnessAPI {
  return new HarnessAPI(() => {}, () => {}, () => {}, translate)
}

function readRPCID(value: unknown): string {
  if (typeof value !== 'object' || value === null || !('rpcId' in value) || typeof value.rpcId !== 'string') {
    throw new Error('web test: request rpcId is missing')
  }
  return value.rpcId
}

function jsonResponse(value: unknown): Response {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: {
      'content-type': 'application/json',
    },
  })
}
