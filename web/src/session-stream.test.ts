import { describe, expect, it } from 'vitest'
import { projectStream } from './session-stream'
import type { SessionEvent } from './types'

describe('projectStream', () => {
  it('folds text and reasoning chunks in Session order', () => {
    expect(projectStream([
      chunk(1, 'reasoning-delta', 'think '),
      chunk(2, 'reasoning-delta', 'again'),
      chunk(3, 'text-delta', 'answer'),
    ])).toEqual({
      text: 'answer',
      reasoning: 'think again',
      streaming: true,
      interrupted: false,
      seq: 3,
    })
  })

  it('removes the draft when a committed assistant message arrives', () => {
    expect(projectStream([
      chunk(1, 'text-delta', 'temporary'),
      event(2, 'assistant/message'),
      event(3, 'turn/end'),
    ])).toBeUndefined()
  })

  it('freezes an uncommitted draft when its turn ends', () => {
    expect(projectStream([
      chunk(1, 'text-delta', 'partial'),
      event(2, 'turn/end'),
    ])).toEqual({
      text: 'partial',
      reasoning: '',
      streaming: false,
      interrupted: true,
      seq: 2,
    })
  })

  it('starts a new draft after a previous interrupted turn', () => {
    expect(projectStream([
      chunk(1, 'text-delta', 'old partial'),
      event(2, 'turn/end'),
      chunk(3, 'text-delta', 'new'),
    ])).toEqual({
      text: 'new',
      reasoning: '',
      streaming: true,
      interrupted: false,
      seq: 3,
    })
  })
})

function chunk(
  seq: number,
  type: 'text-delta' | 'reasoning-delta',
  text: string,
): SessionEvent {
  return {
    type: 'assistant/chunk',
    seq,
    time: seq,
    data: {
      chunk: {
        type,
        text,
      },
    },
  }
}

function event(seq: number, type: string): SessionEvent {
  return {
    type,
    seq,
    time: seq,
  }
}
