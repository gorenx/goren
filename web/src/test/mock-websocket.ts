export class MockWebSocket extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static readonly instances: MockWebSocket[] = []

  readonly url: string
  readyState = MockWebSocket.CONNECTING

  constructor(url: string | URL) {
    super()
    this.url = String(url)
    MockWebSocket.instances.push(this)
  }

  open(): void {
    this.readyState = MockWebSocket.OPEN
    this.dispatchEvent(new Event('open'))
  }

  receive(value: unknown): void {
    this.dispatchEvent(new MessageEvent('message', {
      data: JSON.stringify(value),
    }))
  }

  close(): void {
    if (this.readyState === MockWebSocket.CLOSED) return
    this.readyState = MockWebSocket.CLOSED
    this.dispatchEvent(new CloseEvent('close'))
  }

  static reset(): void {
    MockWebSocket.instances.splice(0)
  }
}
