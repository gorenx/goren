import { useEffect, useRef, useState } from 'react'
import type { ConversationStore } from '../conversation-store'
import { CloseIcon, FolderIcon } from '../icons'
import { useI18n } from '../i18n'

export function WorkspacePicker({ store, disabled, cwd }: { store: ConversationStore; disabled: boolean; cwd?: string }): React.JSX.Element {
  const { translate } = useI18n()
  const [busy, setBusy] = useState(false)
  const pending = useRef<AbortController | undefined>(undefined)
  const name = cwd?.split(/[\\/]/).filter(Boolean).at(-1) ?? cwd

  useEffect(() => () => { pending.current?.abort(); pending.current = undefined }, [])

  const choose = async (): Promise<void> => {
    if (pending.current !== undefined) return
    const controller = new AbortController()
    pending.current = controller
    setBusy(true)
    try {
      await store.pickWorkspace(controller.signal)
    } catch {
      // ConversationStore reports failures; cancellation keeps the current draft.
    } finally {
      if (pending.current === controller) {
        pending.current = undefined
        setBusy(false)
      }
    }
  }

  return <div className="flex min-w-0 items-center">
    <button type="button" className="workspace-button" disabled={disabled || busy} aria-label={translate('workspace.choose')} title={cwd ?? translate('workspace.choose')} aria-haspopup="dialog" aria-busy={busy} onClick={() => void choose()}>
      <FolderIcon size={16} className="shrink-0" />
      <span className="truncate">{busy ? translate('workspace.picking') : name ?? translate('workspace.choose')}</span>
    </button>
    {busy && <button type="button" className="icon-button shrink-0" aria-label={translate('workspace.cancel')} title={translate('workspace.cancel')} onClick={() => pending.current?.abort()}><CloseIcon size={14} /></button>}
  </div>
}
