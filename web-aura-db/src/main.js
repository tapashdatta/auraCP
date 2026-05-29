import { mount } from 'svelte'
// Self-hosted font face declarations (loaded ahead of app.css so the
// browser has the family registrations before any style references them).
// See lib/fonts.css for the FIX rationale (FONTS-NO-SRI-THIRD-PARTY).
import './lib/fonts.css'
import './app.css'
import App from './App.svelte'
// Side-effects: theme writes <html data-theme>; shortcuts register a keydown.
import './lib/theme.svelte.js'

const target = document.getElementById('app')
if (!target) throw new Error('aura-db: #app target not found')

const app = mount(App, { target })

export default app
