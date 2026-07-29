import { createRoot } from 'react-dom/client'
import React from 'react'
import { App } from './App'

const root = document.getElementById('root')
if (!root) throw new Error('root element missing')

createRoot(root).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
