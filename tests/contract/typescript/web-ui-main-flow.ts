import { createRequire } from 'node:module'
import { resolve } from 'node:path'

void (async () => {
const sourceRoot = process.argv[2]
const baseURL = process.argv[3]
const expectedAssistantText = process.argv[4]
if (sourceRoot === undefined || baseURL === undefined || expectedAssistantText === undefined) {
  throw new Error('DSH source root, Go host URL, and expected assistant text are required')
}
const sourceRequire = createRequire(resolve(sourceRoot, 'package.json'))
const { JSDOM } = sourceRequire('jsdom') as { JSDOM: typeof import('jsdom').JSDOM }

const shellResponse = await fetch(`${baseURL}/`)
if (!shellResponse.ok) throw new Error(`Web shell failed with ${String(shellResponse.status)}`)
const shellHTML = await shellResponse.text()
const dom = new JSDOM(shellHTML, {
  url: `${baseURL}/`,
  pretendToBeVisual: true,
  runScripts: 'outside-only',
})
const browserWindow = dom.window
const nativeFetch = globalThis.fetch
browserWindow.fetch = ((input: string | URL | Request, init?: RequestInit): Promise<Response> => {
  const resolved = typeof input === 'string' || input instanceof URL
    ? new URL(input, baseURL)
    : input
  return nativeFetch(resolved, init)
}) as typeof browserWindow.fetch
browserWindow.WebSocket = globalThis.WebSocket as typeof browserWindow.WebSocket
browserWindow.localStorage.setItem('goren.locale', 'zh-CN')

const scriptSource = browserWindow.document.querySelector('script[type="module"][src]')?.getAttribute('src')
if (scriptSource === null || scriptSource === undefined) throw new Error('Web application entry is absent')
const scriptResponse = await nativeFetch(new URL(scriptSource, baseURL))
if (!scriptResponse.ok) throw new Error(`Web application failed with ${String(scriptResponse.status)}`)
browserWindow.eval(await scriptResponse.text())

async function waitFor<T>(read: () => T | undefined, label: string, timeoutMs = 15_000): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let lastError: unknown
  while (Date.now() < deadline) {
    try {
      const value = read()
      if (value !== undefined) return value
    } catch (error) {
      lastError = error
    }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  throw new Error(`${label} timed out${lastError instanceof Error ? `: ${lastError.message}` : ''}`)
}

try {
  const textarea = await waitFor(() => {
    const candidate = browserWindow.document.getElementById('prompt')
    return candidate instanceof browserWindow.HTMLTextAreaElement && !candidate.disabled ? candidate : undefined
  }, 'live composer')

  const languageSelect = browserWindow.document.getElementById('language-switch')
  if (!(languageSelect instanceof browserWindow.HTMLSelectElement)) throw new Error('language selector is absent')
  languageSelect.value = 'zh-CN'
  languageSelect.dispatchEvent(new browserWindow.Event('change', { bubbles: true }))
  await waitFor(() => {
    const newSessionLabel = browserWindow.document.getElementById('new-session')?.textContent ?? ''
    return browserWindow.document.documentElement.lang === 'zh-CN' &&
      browserWindow.localStorage.getItem('goren.locale') === 'zh-CN' &&
      newSessionLabel.includes('新对话')
      ? true
      : undefined
  }, 'Chinese UI language')
  languageSelect.value = 'en'
  languageSelect.dispatchEvent(new browserWindow.Event('change', { bubbles: true }))
  await waitFor(() => {
    const newSessionLabel = browserWindow.document.getElementById('new-session')?.textContent ?? ''
    return browserWindow.document.documentElement.lang === 'en' &&
      browserWindow.localStorage.getItem('goren.locale') === 'en' &&
      newSessionLabel.includes('New conversation')
      ? true
      : undefined
  }, 'English UI language')

  const prompt = 'Reply with exactly OK. Do not call any tool.'
  const completedAssistantCount = browserWindow.document.querySelectorAll('.message.assistant:not(.streaming)').length
  textarea.value = prompt
  textarea.dispatchEvent(new browserWindow.Event('input', { bubbles: true }))
  textarea.dispatchEvent(new browserWindow.KeyboardEvent('keydown', {
    key: 'Enter', code: 'Enter', keyCode: 13, bubbles: true,
  }))
  await waitFor(() => {
    const text = browserWindow.document.getElementById('messages')?.textContent ?? ''
    const completedAssistants = [...browserWindow.document.querySelectorAll('.message.assistant:not(.streaming) .message-body')]
    const latestAssistant = completedAssistants.at(-1)?.textContent?.trim()
    const receivedExpected = expectedAssistantText === '*' ? Boolean(latestAssistant) : text.includes(expectedAssistantText)
    return completedAssistants.length > completedAssistantCount && text.includes(prompt) && receivedExpected
      ? true
      : undefined
  }, 'rendered Agent response', 30_000)

  const previousSession = await waitFor(() => {
    const candidate = browserWindow.document.querySelector('.session-item[aria-current="true"]')
    return candidate instanceof browserWindow.HTMLButtonElement ? candidate : undefined
  }, 'selected completed session')
  const previousSessionID = previousSession.dataset.sessionId
  if (previousSessionID === undefined) throw new Error('selected Session has no identity')

  const newSession = browserWindow.document.getElementById('new-session')
  if (!(newSession instanceof browserWindow.HTMLButtonElement)) throw new Error('New Session action is absent')
  newSession.click()
  await waitFor(() => {
    const active = browserWindow.document.querySelector('.session-item[aria-current="true"]')
    return active instanceof browserWindow.HTMLButtonElement && active.dataset.sessionId !== previousSessionID
      ? active
      : undefined
  }, 'new selected session')

  const previousSessionAfterRefresh = await waitFor(() => {
    const candidate = browserWindow.document.querySelector(`[data-session-id="${previousSessionID}"]`)
    return candidate instanceof browserWindow.HTMLButtonElement ? candidate : undefined
  }, 'previous session row')
  previousSessionAfterRefresh.click()
  await waitFor(() => {
    const text = browserWindow.document.getElementById('messages')?.textContent ?? ''
    const assistant = browserWindow.document.querySelector('.message.assistant .message-body')?.textContent?.trim()
    const restoredExpected = expectedAssistantText === '*' ? Boolean(assistant) : text.includes(expectedAssistantText)
    const active = browserWindow.document.querySelector('.session-item[aria-current="true"]')
    return active instanceof browserWindow.HTMLButtonElement &&
      active.dataset.sessionId === previousSessionID &&
      text.includes(prompt) && restoredExpected
      ? true
      : undefined
  }, 'selected session history')

  const questionComposer = await waitFor(() => {
    const candidate = browserWindow.document.getElementById('prompt')
    return candidate instanceof browserWindow.HTMLTextAreaElement && !candidate.disabled ? candidate : undefined
  }, 'composer before question')
  const assistantCountBeforeQuestion = browserWindow.document.querySelectorAll('.message.assistant:not(.streaming)').length
  const questionPrompt = 'Ask one question before answering.'
  questionComposer.value = questionPrompt
  questionComposer.dispatchEvent(new browserWindow.Event('input', { bubbles: true }))
  questionComposer.dispatchEvent(new browserWindow.KeyboardEvent('keydown', {
    key: 'Enter', code: 'Enter', keyCode: 13, bubbles: true,
  }))

  const questionDock = await waitFor(() => {
    const errorToast = browserWindow.document.querySelector('.toast')?.textContent?.trim()
    if (errorToast !== undefined && errorToast !== '') throw new Error(errorToast)
    const candidate = browserWindow.document.querySelector('[data-question-rpc-id]')
    if (candidate instanceof browserWindow.HTMLElement) return candidate
    throw new Error((browserWindow.document.getElementById('composer-state')?.textContent ?? 'no composer state').trim())
  }, 'question requested by Agent', 30_000)
  const waitingComposer = browserWindow.document.getElementById('prompt')
  if (!(waitingComposer instanceof browserWindow.HTMLTextAreaElement) || !waitingComposer.disabled) {
    throw new Error('ordinary composer remained active while the Agent awaited a question answer')
  }
  const runtimeContextHidden = !(browserWindow.document.getElementById('messages')?.textContent ?? '')
    .includes('Current runtime context.')
  if (!runtimeContextHidden) throw new Error('plugin runtime context was rendered as a user message')

  const architectureOption = questionDock.querySelector('[data-question-option="架构"]')
  const answerButton = questionDock.querySelector('[data-question-submit]')
  if (!(architectureOption instanceof browserWindow.HTMLButtonElement) ||
      !(answerButton instanceof browserWindow.HTMLButtonElement)) {
    throw new Error('question answer controls are incomplete')
  }
  architectureOption.click()
  await waitFor(() => architectureOption.getAttribute('aria-pressed') === 'true' ? true : undefined, 'selected question option')
  answerButton.click()
  await waitFor(() => {
    const completedAssistants = [...browserWindow.document.querySelectorAll('.message.assistant:not(.streaming) .message-body')]
    const latestAssistant = completedAssistants.at(-1)?.textContent ?? ''
    const questionClosed = browserWindow.document.querySelector('[data-question-rpc-id]') === null
    return completedAssistants.length > assistantCountBeforeQuestion &&
      questionClosed && latestAssistant.includes('question answered through the Web UI')
      ? true
      : undefined
  }, 'question answer continuation', 30_000)

  process.stdout.write(`${JSON.stringify({
    booted: true,
    prompted: true,
    selected: true,
    history: true,
    questionAnswered: true,
    localized: true,
    runtimeContextHidden,
  })}\n`)
} finally {
  browserWindow.dispatchEvent(new browserWindow.Event('beforeunload'))
  dom.window.close()
}
})().catch((error: unknown) => {
  console.error(error)
  process.exitCode = 1
})
