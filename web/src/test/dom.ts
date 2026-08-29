export function replaceTextareaValue(
  textarea: HTMLTextAreaElement,
  value: string,
): void {
  const descriptor = Object.getOwnPropertyDescriptor(
    globalThis.HTMLTextAreaElement.prototype,
    'value',
  )
  if (descriptor?.set === undefined) {
    throw new Error('web test: textarea value setter is unavailable')
  }
  descriptor.set.call(textarea, value)
  textarea.dispatchEvent(new Event('input', { bubbles: true }))
}
