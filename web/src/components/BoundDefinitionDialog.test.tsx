import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConversationStore } from '../conversation-store'
import { I18nProvider } from '../i18n'
import type { BoundDefinition, ConversationSnapshot } from '../types'
import { replaceTextareaValue } from '../test/dom'
import { BoundDefinitionDialog } from './BoundDefinitionDialog'

const definition: BoundDefinition = {
  name: 'researcher',
  revision: 2,
  enabled: true,
  systemPrompt: 'Research in the background.',
  extensions: ['report'],
}

function snapshotWith(
  boundDefinitions: readonly BoundDefinition[],
): ConversationSnapshot {
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

  it('creates a complete Definition and preserves empty allow semantics', async () => {
    const createDefinition = vi.fn(async (draft) => ({
      ...draft,
      revision: 1,
    }))
    const store = {
      createBoundDefinition: createDefinition,
      replaceBoundDefinition: vi.fn(),
    } as unknown as ConversationStore
    renderDialog(store, snapshotWith([]))

    await act(async () => {
      replaceInputValue(requiredInput('name'), 'researcher')
      replaceTextareaValue(requiredTextarea('systemPrompt'), 'Research in the background.')
      replaceInputValue(requiredInput('maxDepth'), '0')
      replaceInputValue(requiredInput('extensions'), 'report, memory')
      replaceSelectValue(requiredSelect('toolMode'), 'allow')
    })
    await act(async () => submitButton().click())

    expect(createDefinition).toHaveBeenCalledTimes(1)
    expect(createDefinition).toHaveBeenCalledWith({
      name: 'researcher',
      enabled: true,
      systemPrompt: 'Research in the background.',
      maxDepth: 0,
      toolRestriction: { allow: [] },
      extensions: ['report', 'memory'],
    })
  })

  it('keeps the stable name disabled while replacing a revision', async () => {
    const replaceDefinition = vi.fn(async (_revision, draft) => ({
      ...draft,
      revision: 3,
    }))
    const store = {
      createBoundDefinition: vi.fn(),
      replaceBoundDefinition: replaceDefinition,
    } as unknown as ConversationStore
    renderDialog(store, snapshotWith([definition]))
    const definitionButton = container.querySelector<HTMLButtonElement>(
      '.definition-item',
    )
    if (definitionButton === null) {
      throw new Error('web test: Definition selection is missing')
    }
    await act(async () => definitionButton.click())
    const nameInput = requiredInput('name')
    expect(nameInput.disabled).toBe(true)
    const enabled = container.querySelector<HTMLInputElement>(
      'input[type="checkbox"]',
    )
    if (enabled === null) throw new Error('web test: enabled control is missing')
    await act(async () => enabled.click())
    await act(async () => submitButton().click())

    expect(replaceDefinition).toHaveBeenCalledTimes(1)
    expect(replaceDefinition).toHaveBeenCalledWith(2, {
      name: 'researcher',
      enabled: false,
      systemPrompt: 'Research in the background.',
      extensions: ['report'],
    })
  })

  function renderDialog(
    store: ConversationStore,
    snapshot: ConversationSnapshot,
  ): void {
    act(() => {
      mountedRoot = createRoot(container)
      mountedRoot.render(
        <I18nProvider>
          <BoundDefinitionDialog
            store={store}
            snapshot={snapshot}
            onClose={() => {}}
          />
        </I18nProvider>,
      )
    })
  }

  function requiredInput(name: string): HTMLInputElement {
    const input = container.querySelector<HTMLInputElement>(
      `input[name="${name}"]`,
    )
    if (input === null) throw new Error(`web test: input ${name} is missing`)
    return input
  }

  function requiredTextarea(name: string): HTMLTextAreaElement {
    const textarea = container.querySelector<HTMLTextAreaElement>(
      `textarea[name="${name}"]`,
    )
    if (textarea === null) {
      throw new Error(`web test: textarea ${name} is missing`)
    }
    return textarea
  }

  function requiredSelect(name: string): HTMLSelectElement {
    const select = container.querySelector<HTMLSelectElement>(
      `select[name="${name}"]`,
    )
    if (select === null) throw new Error(`web test: select ${name} is missing`)
    return select
  }

  function submitButton(): HTMLButtonElement {
    const button = container.querySelector<HTMLButtonElement>(
      'button[type="submit"]',
    )
    if (button === null) throw new Error('web test: submit button is missing')
    return button
  }
})

function replaceInputValue(input: HTMLInputElement, value: string): void {
  const descriptor = Object.getOwnPropertyDescriptor(
    globalThis.HTMLInputElement.prototype,
    'value',
  )
  if (descriptor?.set === undefined) {
    throw new Error('web test: input value setter is unavailable')
  }
  descriptor.set.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function replaceSelectValue(select: HTMLSelectElement, value: string): void {
  const descriptor = Object.getOwnPropertyDescriptor(
    globalThis.HTMLSelectElement.prototype,
    'value',
  )
  if (descriptor?.set === undefined) {
    throw new Error('web test: select value setter is unavailable')
  }
  descriptor.set.call(select, value)
  select.dispatchEvent(new Event('change', { bubbles: true }))
}
