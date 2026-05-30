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
  { type:'wordpress',    ic:'◆', name:'WordPress',    desc:'WordPress via wp-cli; database provisioned automatically; HTTPS managed.' },
  { type:'php',          ic:'php', name:'PHP',         desc:'PHP-FPM pool per site (PHP 8.3+, dedicated UID, Unix socket).' },
  { type:'nodejs',       ic:'▲', name:'Node.js',       desc:'systemd unit behind an nginx reverse proxy.' },
  { type:'static',       ic:'▤', name:'Static HTML',   desc:'Static file server with optional nginx response caching.' },
  { type:'python',       ic:'py', name:'Python',        desc:'gunicorn or uvicorn as a per-site systemd unit.' },
  { type:'reverseproxy', ic:'⇄', name:'Reverse Proxy', desc:'TLS termination + optional caching in front of any upstream.' },
]

export const detailTabs = [
  { id:'settings', label:'Settings' },
  { id:'vhost', label:'Vhost' },
  { id:'databases', label:'DB' },
  { id:'security', label:'Security' },
  { id:'sshftp', label:'FTP' },
  { id:'files', label:'Directory' },
  { id:'cron', label:'Cron' },
  { id:'logs', label:'Logs' },
]
