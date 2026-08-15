import { useEffect, useState, useSyncExternalStore } from 'react'
import { ConversationStore } from './conversation-store'
import { Sidebar } from './components/Sidebar'
import { ConversationPane } from './components/ConversationPane'
import { DetailsPanel } from './components/DetailsPanel'
import { CloseIcon } from './icons'
import { CredentialDialog } from './components/CredentialDialog'

export function App(): React.JSX.Element {
  const [store] = useState(() => new ConversationStore())
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [credentialsOpen, setCredentialsOpen] = useState(false)
  const [onboardingDismissed, setOnboardingDismissed] = useState(false)
  const snapshot = useSyncExternalStore(store.subscribe, store.snapshot)

  useEffect(() => {
    void store.start()
    const close = (): void => store.dispose()
    globalThis.addEventListener('beforeunload', close)
    return () => {
      globalThis.removeEventListener('beforeunload', close)
      store.dispose()
    }
  }, [store])

  useEffect(() => {
    if (snapshot.credentialLoaded && snapshot.credential?.configured === false && snapshot.credential.writable && !onboardingDismissed) {
      setCredentialsOpen(true)
    }
  }, [onboardingDismissed, snapshot.credential, snapshot.credentialLoaded])

  const closeCredentials = (): void => {
    setCredentialsOpen(false)
    setOnboardingDismissed(true)
  }

  return (
    <div className="app-grid" data-sidebar-collapsed={sidebarCollapsed || undefined}>
      <Sidebar store={store} snapshot={snapshot} collapsed={sidebarCollapsed} onToggle={() => setSidebarCollapsed(value => !value)} />
      <ConversationPane store={store} snapshot={snapshot} onOpenCredentials={() => setCredentialsOpen(true)} />
      <DetailsPanel store={store} snapshot={snapshot} />
      {credentialsOpen && <CredentialDialog store={store} snapshot={snapshot} onClose={closeCredentials} />}
      {snapshot.error !== undefined && (
        <div className="toast" role="alert">
          <span className="min-w-0 flex-1">{snapshot.error}</span>
          <button type="button" className="grid h-7 w-7 place-items-center rounded-full text-white/70 hover:bg-white/10 hover:text-white" aria-label="关闭错误提示" onClick={() => store.dismissError()}>
            <CloseIcon size={15} />
          </button>
        </div>
      )}
    </div>
  )
}
