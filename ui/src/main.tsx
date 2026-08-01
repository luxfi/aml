import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { GuiProvider } from '@hanzogui/core'

import config, { adoptStyles, theme } from './gui'
import './app.css'
import { App } from './app'

const root = document.getElementById('root')
if (!root) throw new Error('root element missing')

// Before the first render, so the first paint is styled. See ./gui.ts for why
// this is a CSSOM call and not the <style> element the kit would write.
adoptStyles()

createRoot(root).render(
  <StrictMode>
    <GuiProvider config={config} defaultTheme={theme} disableInjectCSS>
      <App />
    </GuiProvider>
  </StrictMode>,
)
