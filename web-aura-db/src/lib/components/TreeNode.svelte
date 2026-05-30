<script>
  import EngineGlyph from './EngineGlyph.svelte'
  import StatusDot from './StatusDot.svelte'
  import Icon from './Icon.svelte'
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
   *   onActivate?: ()=>void,
   * }} */
  let { node, depth = 0, ariaLevel = 1, selected = false, expanded = false, onToggle, onSelect, onActivate } = $props()

  const canExpand = $derived(node.kind === 'connection' || node.kind === 'schema')

  function click() {
    if (canExpand) onToggle?.()
    onSelect?.()
  }
  function dblclick() {
    onActivate?.()
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
  ondblclick={dblclick}
  onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); click() } }}
>
  {#if canExpand}
    <span class="tree-row__chev {expanded ? 'tree-row__chev--open' : ''}">
      <Icon name="chevR" size={14} />
    </span>
  {:else}
    <span class="tree-row__chev"></span>
  {/if}

  {#if node.kind === 'connection'}
    <EngineGlyph engine={node.engine || 'postgres'} size={16} />
  {:else if node.kind === 'table'}
    <span class="tree-row__glyph tree-row__glyph--obj"><Icon name="table" size={15} /></span>
  {:else if node.kind === 'view'}
    <span class="tree-row__glyph tree-row__glyph--obj"><Icon name="view" size={15} /></span>
  {:else if node.kind === 'function'}
    <span class="tree-row__glyph tree-row__glyph--obj"><Icon name="terminal" size={15} /></span>
  {/if}

  <span class="tree-row__label">{node.label}</span>

  {#if node.readOnly}
    <span class="tree-row__meta">{t('tree.readonly')}</span>
  {/if}

  {#if selected && node.kind === 'connection'}
    <span class="tree-row__check"><Icon name="check" size={14} /></span>
  {:else if node.status && node.kind === 'connection'}
    <StatusDot state={node.status} />
  {/if}
</div>
