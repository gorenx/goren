import { afterEach } from 'vitest'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

afterEach(() => {
  document.body.replaceChildren()
  globalThis.localStorage.clear()
})
