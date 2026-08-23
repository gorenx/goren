import { describe, expect, it } from 'vitest'
import { mergeEvents } from './session-events'
import type { SessionEvent } from './types'

describe('mergeEvents', () => {
  it('orders history and live events while keeping one event per sequence', () => {
    const history = [event(1, 'turn/start'), event(3, 'assistant/chunk')]
    const live = [event(3, 'assistant/chunk'), event(2, 'step/start')]

    expect(mergeEvents(history, live)).toEqual([
      event(1, 'turn/start'),
      event(2, 'step/start'),
      event(3, 'assistant/chunk'),
    ])
  })
})

function event(seq: number, type: string): SessionEvent {
  return {
    type,
    seq,
    time: seq,
  }
}
