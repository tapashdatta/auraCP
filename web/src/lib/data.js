// Live data only — sites come from the auracpd API. No mock/demo data.
import { apiFetch } from './api.js'

export async function fetchSites() {
  try {
    const r = await apiFetch('/api/sites')
    if (!r.ok) return []
    const data = await r.json()
    return Array.isArray(data) ? data : []
  } catch {
    return []
  }
}

export const siteTypes = [
  { type:'wordpress',    ic:'◆', name:'WordPress',    desc:'Managed WP with wp-cli, auto database & one-click SSL.' },
  { type:'php',          ic:'php', name:'PHP',         desc:'PHP-FPM pool per site. PHP 8.3+, isolated UID + Unix socket.' },
  { type:'nodejs',       ic:'▲', name:'Node.js',       desc:'systemd-managed app behind nginx reverse proxy.' },
  { type:'static',       ic:'▤', name:'Static HTML',   desc:'Edge-cached file server. Zero runtime, instant loads.' },
  { type:'python',       ic:'py', name:'Python',        desc:'gunicorn/uvicorn via systemd. Python 3.12 ready.' },
  { type:'reverseproxy', ic:'⇄', name:'Reverse Proxy', desc:'Proxy any upstream with TLS termination & caching.' },
]

export const detailTabs = [
  { id:'settings', label:'Settings' },
  { id:'vhost', label:'Vhost' },
  { id:'databases', label:'Databases' },
  { id:'cache', label:'Cache' },
  { id:'ssl', label:'SSL/TLS' },
  { id:'security', label:'Security' },
  { id:'sshftp', label:'SSH/FTP' },
  { id:'files', label:'File Manager' },
  { id:'cron', label:'Cron Jobs' },
  { id:'logs', label:'Logs' },
]
