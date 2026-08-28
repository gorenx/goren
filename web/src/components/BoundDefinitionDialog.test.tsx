import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConversationStore } from '../conversation-store'
import { I18nProvider } from '../i18n'
import type { BoundDefinition, BoundExtensionOption, BoundToolOption, ConversationSnapshot } from '../types'
import { replaceTextareaValue } from '../test/dom'
import { BoundDefinitionDialog } from './BoundDefinitionDialog'

const definition: BoundDefinition = {
  name: 'researcher',
  revision: 2,
  enabled: true,
  systemPrompt: 'Research in the background.',
  agentOptions: {
    provider: 'deepseek',
    model: 'deepseek-chat',
    maxTokens: 4096,
  },
  maxDepth: 3,
  extensions: ['report'],
}

const boundTools: readonly BoundToolOption[] = [
  {
    name: 'delegate',
    description: 'Start a child agent.',
  },
  {
    name: 'list_agents',
    description: 'List child agents.',
  },
]

const boundExtensions: readonly BoundExtensionOption[] = [
  {
    name: 'memory',
  },
  {
    name: 'report',
  },
]

function snapshotWith(boundDefinitions: readonly BoundDefinition[]): ConversationSnapshot {
  return {
    phase: 'ready',
    sessions: [],
    loadingMoreSessions: false,
    events: new Map(),
    histories: new Map(),
    streams: new Map(),
    pendingQuestions: new Map(),
    localTitles: new Map(),
    creatingSession: false,
    onlineDownlinks: 2,
    composerState: 'composer.ready',
    credentialLoaded: true,
    boundDefinitionsLoaded: true,
    boundDefinitions,
    boundToolsState: 'ready',
    boundTools,
    boundExtensionsState: 'ready',
    boundExtensions,
  }
}

describe('BoundDefinitionDialog', () => {
  let container: HTMLDivElement
  let mountedRoot: Root | undefined

  beforeEach(() => {
    container = document.createElement('div')
    document.body.append(container)
  })

  afterEach(() => {
    if (mountedRoot !== undefined) {
      act(() => mountedRoot?.unmount())
      mountedRoot = undefined
    }
    container.remove()
  })

  it('creates a Definition with an intentional empty Tool selection', async () => {
    const createDefinition = vi.fn(async draft => ({
      ...draft,
      revision: 1,
    }))
    renderDialog(storeWith({ createBoundDefinition: createDefinition }), snapshotWith([]))

    await fillRequiredFields()
    await act(async () => requiredRadio('allow').click())
    await act(async () => submitButton().click())

    expect(createDefinition).toHaveBeenCalledWith({
      name: 'researcher',
      enabled: true,
      systemPrompt: 'Research in the background.',
      toolRestriction: {
        allow: [],
      },
      extensions: [],
    })
  })

  it('keeps the stable name and hidden runtime overrides while replacing', async () => {
    const replaceDefinition = vi.fn(async (_revision, draft) => ({
      ...draft,
      revision: 3,
    }))
    renderDialog(storeWith({ replaceBoundDefinition: replaceDefinition }), snapshotWith([definition]))
    await selectDefinition()

    expect(requiredInput('name').disabled).toBe(true)
    const enabled = container.querySelector<HTMLInputElement>('.definition-enabled input')
    if (enabled === null) throw new Error('web test: enabled control is missing')
    await act(async () => enabled.click())
    await act(async () => submitButton().click())

    expect(replaceDefinition).toHaveBeenCalledWith(2, {
      name: 'researcher',
      enabled: false,
      systemPrompt: 'Research in the background.',
      agentOptions: {
        provider: 'deepseek',
        model: 'deepseek-chat',
        maxTokens: 4096,
      },
      maxDepth: 3,
      extensions: ['report'],
    })
  })

  it('chooses Tools through the searchable catalog', async () => {
    const createDefinition = vi.fn(async draft => ({
      ...draft,
      revision: 1,
    }))
    renderDialog(storeWith({ createBoundDefinition: createDefinition }), snapshotWith([]))
    await fillRequiredFields()
    await act(async () => requiredRadio('allow').click())
    await act(async () => buttonWithText('+ Choose tools').click())
    await act(async () => replaceInputValue(requiredSearch('Search names or descriptions…'), 'delegate'))
    await act(async () => requiredTool('allow', 'delegate').click())
    expect(container.querySelectorAll('.catalog-picker-list label')).toHaveLength(1)
    await act(async () => submitButton().click())

    expect(createDefinition).toHaveBeenCalledWith(expect.objectContaining({
      toolRestriction: {
        allow: ['delegate'],
      },
    }))
  })

  it('normalizes an existing Allow and Deny restriction to its effective selection', async () => {
    const replaceDefinition = vi.fn(async (_revision, draft) => ({
      ...draft,
      revision: 3,
    }))
    const restricted: BoundDefinition = {
      ...definition,
      toolRestriction: {
        allow: ['delegate', 'list_agents'],
        deny: ['delegate'],
      },
    }
    renderDialog(storeWith({ replaceBoundDefinition: replaceDefinition }), snapshotWith([restricted]))
    await selectDefinition()

    expect(requiredRadio('allow').checked).toBe(true)
    expect(container.textContent).toContain('list_agents')
    expect(container.textContent).not.toContain('delegateStart a child agent')
    await act(async () => replaceTextareaValue(requiredTextarea('systemPrompt'), 'Updated prompt.'))
    await act(async () => submitButton().click())

    expect(replaceDefinition).toHaveBeenCalledWith(2, expect.objectContaining({
      systemPrompt: 'Updated prompt.',
      toolRestriction: {
        allow: ['list_agents'],
      },
    }))
  })

  it('adds Extensions and preserves the selected installation order', async () => {
    const createDefinition = vi.fn(async draft => ({
      ...draft,
      revision: 1,
    }))
    renderDialog(storeWith({ createBoundDefinition: createDefinition }), snapshotWith([]))
    await fillRequiredFields()
    await act(async () => buttonWithText('+ Add Extension').click())
    await act(async () => buttonWithText('+memory').click())
    await act(async () => buttonWithText('+report').click())
    await act(async () => buttonWithLabel('Move Extension report up').click())
    expect([...container.querySelectorAll('.extension-stack code')].map(node => node.textContent)).toEqual(['report', 'memory'])
    await act(async () => submitButton().click())

    expect(createDefinition).toHaveBeenCalledWith(expect.objectContaining({
      extensions: ['report', 'memory'],
    }))
  })

  it('loads both catalogs on open and retries failures locally', async () => {
    const refreshBoundTools = vi.fn(async () => {})
    const refreshBoundExtensions = vi.fn(async () => {})
    const snapshot: ConversationSnapshot = {
      ...snapshotWith([]),
      boundToolsState: 'failed',
      boundTools: [],
      boundToolsError: 'tool directory unavailable',
      boundExtensionsState: 'failed',
      boundExtensions: [],
      boundExtensionsError: 'extension directory unavailable',
    }
    renderDialog(storeWith({ refreshBoundTools, refreshBoundExtensions }), snapshot)

    expect(refreshBoundTools).toHaveBeenCalledTimes(1)
    expect(refreshBoundExtensions).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('tool directory unavailable')
    expect(container.textContent).toContain('extension directory unavailable')
    const reloadButtons = [...container.querySelectorAll<HTMLButtonElement>('button')]
      .filter(button => button.textContent === 'Reload')
    expect(reloadButtons).toHaveLength(2)
    await act(async () => reloadButtons[0].click())
    await act(async () => reloadButtons[1].click())
    expect(refreshBoundTools).toHaveBeenCalledTimes(2)
    expect(refreshBoundExtensions).toHaveBeenCalledTimes(2)
  })

  function storeWith(overrides: Partial<ConversationStore> = {}): ConversationStore {
    return {
      createBoundDefinition: vi.fn(),
      replaceBoundDefinition: vi.fn(),
      refreshBoundTools: vi.fn(),
      refreshBoundExtensions: vi.fn(),
      ...overrides,
    } as unknown as ConversationStore
  }

  function renderDialog(store: ConversationStore, snapshot: ConversationSnapshot): void {
    act(() => {
      mountedRoot = createRoot(container)
      mountedRoot.render(<I18nProvider><BoundDefinitionDialog store={store} snapshot={snapshot} onClose={() => {}} /></I18nProvider>)
    })
  }

  async function fillRequiredFields(): Promise<void> {
    await act(async () => {
      replaceInputValue(requiredInput('name'), 'researcher')
      replaceTextareaValue(requiredTextarea('systemPrompt'), 'Research in the background.')
    })
  }

  async function selectDefinition(): Promise<void> {
    const button = container.querySelector<HTMLButtonElement>('.definition-item')
    if (button === null) throw new Error('web test: Definition selection is missing')
    await act(async () => button.click())
  }

  function requiredInput(name: string): HTMLInputElement {
    const input = container.querySelector<HTMLInputElement>(`input[name="${name}"]`)
    if (input === null) throw new Error(`web test: input ${name} is missing`)
    return input
  }

  function requiredTextarea(name: string): HTMLTextAreaElement {
    const textarea = container.querySelector<HTMLTextAreaElement>(`textarea[name="${name}"]`)
    if (textarea === null) throw new Error(`web test: textarea ${name} is missing`)
    return textarea
  }

  function requiredRadio(value: string): HTMLInputElement {
    const input = container.querySelector<HTMLInputElement>(`input[name="toolMode"][value="${value}"]`)
    if (input === null) throw new Error(`web test: Tool policy ${value} is missing`)
    return input
  }

  function requiredTool(fieldName: string, value: string): HTMLInputElement {
    const input = container.querySelector<HTMLInputElement>(`input[name="${fieldName}"][value="${value}"]`)
    if (input === null) throw new Error(`web test: ${fieldName} Tool ${value} is missing`)
    return input
  }

  function requiredSearch(placeholder: string): HTMLInputElement {
    const input = container.querySelector<HTMLInputElement>(`input[type="search"][placeholder="${placeholder}"]`)
    if (input === null) throw new Error(`web test: search ${placeholder} is missing`)
    return input
  }

  function buttonWithText(label: string): HTMLButtonElement {
    const button = [...container.querySelectorAll<HTMLButtonElement>('button')].find(candidate => candidate.textContent === label)
    if (button === undefined) throw new Error(`web test: button ${label} is missing`)
    return button
  }

  function buttonWithLabel(label: string): HTMLButtonElement {
    const button = container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)
    if (button === null) throw new Error(`web test: button ${label} is missing`)
    return button
  }

  function submitButton(): HTMLButtonElement {
    const button = container.querySelector<HTMLButtonElement>('button[type="submit"]')
    if (button === null) throw new Error('web test: submit button is missing')
    return button
  }
})

function replaceInputValue(input: HTMLInputElement, value: string): void {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis.HTMLInputElement.prototype, 'value')
  if (descriptor?.set === undefined) throw new Error('web test: input value setter is unavailable')
  descriptor.set.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}
