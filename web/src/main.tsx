import { createRoot } from 'react-dom/client'
import { App } from './App'
import { I18nProvider } from './i18n'
import './styles.css'

const rootElement = document.getElementById('root')
if (rootElement === null) throw new Error('web: missing #root')
createRoot(rootElement).render(<I18nProvider><App /></I18nProvider>)
