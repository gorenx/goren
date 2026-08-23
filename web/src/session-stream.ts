import type { SessionEvent, StreamDraft } from './types'
import { isRecord, recordString } from './types'

// projectStream rebuilds the transient assistant draft from the canonical,
// ordered Session event window. Callers must deduplicate events by seq first.
export function projectStream(events: readonly SessionEvent[]): StreamDraft | undefined {
  let draft: StreamDraft | undefined
  for (const event of events) {
    if (event.type === 'assistant/chunk') {
      draft = appendChunk(draft, event)
      continue
    }
    if (event.type === 'assistant/message') {
      draft = undefined
      continue
    }
    if (event.type === 'turn/end' && draft !== undefined) {
      draft = {
        ...draft,
        streaming: false,
        interrupted: true,
        seq: event.seq,
      }
    }
  }
  return draft
}

function appendChunk(
  current: StreamDraft | undefined,
  event: SessionEvent,
): StreamDraft | undefined {
  if (!isRecord(event.data) || !isRecord(event.data.chunk)) return current
  const chunkType = recordString(event.data.chunk, 'type')
  const text = recordString(event.data.chunk, 'text')
  if (text === undefined || (chunkType !== 'text-delta' && chunkType !== 'reasoning-delta')) {
    return current
  }
  const draft = current?.streaming === true
    ? { ...current, seq: event.seq }
    : {
        text: '',
        reasoning: '',
        streaming: true,
        interrupted: false,
        seq: event.seq,
      }
  if (chunkType === 'text-delta') draft.text += text
  else draft.reasoning += text
  return draft
}
