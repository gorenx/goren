import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { ConversationStore } from './conversation-store'
import { Sidebar } from './components/Sidebar'
import { ConversationPane } from './components/ConversationPane'
import { DetailsPanel } from './components/DetailsPanel'
import { CloseIcon } from './icons'
import { CredentialDialog } from './components/CredentialDialog'
import { SettingsDialog } from './components/SettingsDialog'
import { CompanionDialog } from './components/CompanionDialog'
import { MCPDialog } from './components/MCPDialog'
import { useI18n } from './i18n'

export function App(): React.JSX.Element {
  const { translate } = useI18n()
  const translateRef = useRef(translate)
  translateRef.current = translate
  const [store] = useState(() => new ConversationStore((messageKey, values) => translateRef.current(messageKey, values)))
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [credentialsOpen, setCredentialsOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [companionsOpen, setCompanionsOpen] = useState(false)
  const [mcpOpen, setMCPOpen] = useState(false)
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
    if (
      snapshot.credentialLoaded &&
      snapshot.credential?.configured === false &&
      snapshot.credential.writable &&
      !onboardingDismissed &&
      !settingsOpen
    ) {
      setCredentialsOpen(true)
    }
  }, [onboardingDismissed, settingsOpen, snapshot.credential, snapshot.credentialLoaded])

  const closeCredentials = (): void => {
    setCredentialsOpen(false)
    setOnboardingDismissed(true)
  }

  return (
    <div className="app-grid" data-sidebar-collapsed={sidebarCollapsed || undefined}>
    <Sidebar store={store} snapshot={snapshot} collapsed={sidebarCollapsed} onToggle={() => setSidebarCollapsed(value => !value)} onOpenCompanions={() => setCompanionsOpen(true)} onOpenMCP={() => setMCPOpen(true)} onOpenSettings={() => setSettingsOpen(true)} />
      <ConversationPane store={store} snapshot={snapshot} onOpenCredentials={() => setCredentialsOpen(true)} />
      <DetailsPanel store={store} snapshot={snapshot} />
    {credentialsOpen && <CredentialDialog store={store} snapshot={snapshot} onClose={closeCredentials} />}
    {settingsOpen && <SettingsDialog onClose={() => { setSettingsOpen(false); void store.refreshCredential().catch(() => { /* The store reports the failure. */ }) }} onCredentialChanged={() => store.refreshCredential()} />}
    {companionsOpen && <CompanionDialog key={snapshot.currentSessionId} sessionId={snapshot.currentSessionId} onClose={() => setCompanionsOpen(false)} />}
    {mcpOpen && <MCPDialog key={snapshot.currentSessionId} sessionId={snapshot.currentSessionId} onClose={() => setMCPOpen(false)} onOpenSettings={() => { setMCPOpen(false); setSettingsOpen(true) }} />}
      {snapshot.error !== undefined && (
        <div className="toast" role="alert">
          <span className="min-w-0 flex-1">{snapshot.error}</span>
          <button type="button" className="grid h-7 w-7 place-items-center rounded-full text-white/70 hover:bg-white/10 hover:text-white" aria-label={translate('app.dismissError')} onClick={() => store.dismissError()}>
            <CloseIcon size={15} />
          </button>
        </div>
      )}
    </div>
  )
}
