<script>
  import EngineGlyph from './EngineGlyph.svelte'
  import StatusDot from './StatusDot.svelte'
  import { icons } from '../icons.js'
  import { t } from '../strings.js'

  /** @typedef {{
   *   kind:'connection'|'schema'|'table'|'view'|'function',
   *   id:string,
   *   label:string,
   *   engine?:string,
   *   readOnly?:boolean,
   *   status?:'ok'|'warn'|'down'|'idle',
   *   children?:any[]
   * }} Node */

  /** @type {{
   *   node: Node,
   *   depth?: number,
   *   ariaLevel?: number,
   *   selected?: boolean,
   *   expanded?: boolean,
   *   onToggle?: ()=>void,
   *   onSelect?: ()=>void,
   * }} */
  let { node, depth = 0, ariaLevel = 1, selected = false, expanded = false, onToggle, onSelect } = $props()

  const canExpand = $derived(node.kind === 'connection' || node.kind === 'schema')

  function click() {
    if (canExpand) onToggle?.()
    onSelect?.()
  }
</script>

<div
  class="tree-row {selected ? 'tree-row--selected' : ''}"
  style="padding-left:{6 + depth * 12}px"
  role="treeitem"
  tabindex="0"
  aria-selected={selected}
  aria-expanded={canExpand ? expanded : undefined}
  aria-level={ariaLevel}
  data-conn-id={node.kind === 'connection' ? node.id : (node.id || '').split(':')[0]}
  data-kind={node.kind}
  onclick={click}
  onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); click() } }}
>
  {#if canExpand}
    <span class="tree-row__chev {expanded ? 'tree-row__chev--open' : ''}">
      <svg width="10" height="10" viewBox="0 0 12 12" aria-hidden="true">
        <path d={icons.chevron} fill="currentColor" />
      </svg>
    </span>
  {:else}
    <span class="tree-row__chev"></span>
  {/if}

  {#if node.kind === 'connection'}
    <EngineGlyph engine={node.engine || 'postgres'} />
  {/if}

  <span class="tree-row__label">{node.label}</span>

  {#if node.readOnly}
    <span class="tree-row__meta">{t('tree.readonly')}</span>
  {/if}

  {#if node.status && node.kind === 'connection'}
    <StatusDot state={node.status} />
  {/if}
</div>
