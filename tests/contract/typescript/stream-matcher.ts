type MatcherOptions<Frame> = {
  timeoutMs: number
  maxFrames: number
  describeFrame?: (frame: Frame) => string
}

/**
 * Builds a predicate reader that retains unmatched frames for later checks.
 * Each wait owns and clears its timeout so successful contract runs do not
 * keep Node alive for the full failure deadline.
 */
export const createFrameMatcher = <Frame>(
  iterator: AsyncIterator<Frame>,
  options: MatcherOptions<Frame>,
): ((predicate: (frame: Frame) => boolean, label: string) => Promise<Frame>) => {
  const pending: Frame[] = []
  return async (predicate, label) => {
    const pendingIndex = pending.findIndex(predicate)
    if (pendingIndex >= 0) return pending.splice(pendingIndex, 1)[0] as Frame

    for (let index = 0; index < options.maxFrames; index++) {
      let timeout: ReturnType<typeof setTimeout> | undefined
      const timeoutError = new Error(`${label} timed out`)
      let outcome: IteratorResult<Frame>
      try {
        outcome = await Promise.race([
          iterator.next(),
          new Promise<never>((_resolve, reject) => {
            timeout = setTimeout(() => reject(timeoutError), options.timeoutMs)
          }),
        ])
      } catch (error) {
        if (error !== timeoutError) throw error
        const observed = options.describeFrame === undefined
          ? `${pending.length} unmatched frame(s)`
          : pending.map(options.describeFrame).join(', ')
        throw new Error(`${label} timed out; buffered: [${observed}]`)
      } finally {
        if (timeout !== undefined) clearTimeout(timeout)
      }
      if (outcome.done === true) throw new Error(`${label}: stream ended`)
      if (predicate(outcome.value)) return outcome.value
      pending.push(outcome.value)
    }
    throw new Error(`${label}: frame budget exhausted`)
  }
}
