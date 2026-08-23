import type { SessionEvent } from './types'

// mergeEvents produces the canonical browser window for one Session. Sequence
// numbers are immutable identities, so a replayed event cannot create a second
// projection input.
export function mergeEvents(
  ...windows: readonly (readonly SessionEvent[])[]
): SessionEvent[] {
  const bySequence = new Map<number, SessionEvent>()
  for (const events of windows) {
    for (const event of events) {
      if (!bySequence.has(event.seq)) bySequence.set(event.seq, event)
    }
  }
  return [...bySequence.values()].sort((left, right) => left.seq - right.seq)
}
