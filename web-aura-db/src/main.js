import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'
// Side-effects: theme writes <html data-theme>; shortcuts register a keydown.
import './lib/theme.svelte.js'

const target = document.getElementById('app')
if (!target) throw new Error('aura-db: #app target not found')

const app = mount(App, { target })

export default app
