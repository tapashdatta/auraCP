<script>
  import { fmtCost, fmtRows, fmtMs, hotspotFlags } from '../../sqlEditor/explainFormat.js'
  import Pill from '../Pill.svelte'

  /**
   * @type {{
   *   node: any,
   *   engine: string,
   *   analyzed: boolean,
   *   planWarnings?: string[],
   *   rawSlice?: any,
   * }}
   */
  let { node, engine, analyzed, planWarnings = [], rawSlice = null } = $props()

  const isMaria = $derived(engine === 'mariadb')
  const m = $derived(node?.metrics || {})
  const hs = $derived(hotspotFlags(m))
  // FIX CORR-3 (PR #14): PG nodes can be planned but skipped at runtime
  // (e.g. one branch of an Append, an uninstantiated CTE). When ANALYZE
  // ran but the node never executed (loops === 0), all "Actual" metrics
  // are misleadingly zero. Render em-dashes and a "not executed" badge.
  const notExecuted = $derived(analyzed && !isMaria && (m.loops === 0 || m.loops == null))

  function copyKindPath(ev) {
    try {
      // Buttons in node detail don't carry the path id (NodeDetail just
      // sees the node) — caller can extend via prop in a follow-up.
      navigator.clipboard?.writeText(node?.kind || '')
    } catch { /* ignore */ }
    // visual ack is left to the browser
  }

  function rawJson(v) {
    try { return JSON.stringify(v, null, 2) } catch { return String(v) }
  }
</script>

<!-- FIX (PR #14.5 A11Y-5 routed): the static aria-label="Plan node details"
     gave SR users no context as the selected node changed. Use
     aria-labelledby pointing at the kind heading when a node is
     selected; fall back to a static label when empty. -->
{#if !node}
  <aside class="node-detail" role="region" aria-label="Plan node details">
    <div class="node-detail__empty">
      <p class="node-detail__emptyTitle">Select a node in the tree to inspect</p>
      <p class="node-detail__emptyHint">
        <kbd>↑</kbd> <kbd>↓</kbd> to traverse · <kbd>Enter</kbd> to expand
      </p>
    </div>
  </aside>
{:else}
  <aside class="node-detail" role="region" aria-labelledby="node-detail-kind" aria-live="polite" aria-atomic="false">
    <span class="explain-inspector__sr-only">
      Selected: {node.kind}{node.relation ? ' on ' + node.relation : ''}
    </span>

    <header class="node-detail__head">
      <h3 id="node-detail-kind" class="node-detail__kind">{node.kind || 'Node'}</h3>
      <button class="node-detail__copy" type="button" onclick={copyKindPath} title="Copy node kind" aria-label="Copy node kind">⧉</button>
    </header>

    {#if node.relation || node.schema || node.alias || notExecuted}
      <div class="node-detail__subtitle">
        {#if node.schema}<span class="node-detail__pill">{node.schema}</span>{/if}
        {#if node.relation}<span class="node-detail__pill node-detail__pill--mono">{node.relation}</span>{/if}
        {#if node.alias}<span class="node-detail__pill node-detail__pill--dim">AS {node.alias}</span>{/if}
        {#if notExecuted}
          <span
            class="node-detail__pill node-detail__pill--warn"
            data-testid="node-not-executed"
            title="ANALYZE ran but this node was never executed (loops=0)"
          >not executed</span>
        {/if}
      </div>
    {/if}

    <dl class="node-detail__grid">
      <div class="node-detail__row">
        <dt>Relation</dt>
        <dd>{node.relation || '—'}</dd>
      </div>
      <div class="node-detail__row">
        <dt>Schema</dt>
        <dd>{isMaria ? '—' : (node.schema || '—')}</dd>
      </div>
      <div class="node-detail__row">
        <dt>Index</dt>
        <dd>{node.index || '—'}</dd>
      </div>
      <div class="node-detail__row">
        <dt>Join</dt>
        <dd>{isMaria ? '—' : (node.joinType || '—')}</dd>
      </div>
      {#if node.filter}
        <div class="node-detail__row node-detail__row--wide">
          <dt>
            {engine === 'postgres' ? 'Predicate' : 'Filter'}
            {#if engine === 'postgres'}
              <span class="node-detail__hint" title="Postgres collapses Filter / Index Cond / Hash Cond / Merge Cond / Recheck Cond into one slot">(?)</span>
            {/if}
          </dt>
          <dd><pre class="node-detail__filter">{node.filter}</pre></dd>
        </div>
      {/if}
    </dl>

    <h4 class="node-detail__sectionH">Metrics</h4>
    <table class="data node-detail__metrics">
      <thead>
        <tr>
          <th scope="col">Metric</th>
          <th scope="col" class="num">Expected</th>
          <th scope="col" class="num">Actual</th>
          <th scope="col">Hot?</th>
        </tr>
      </thead>
      <tbody>
        <tr>
          <td>Cost (start → total)</td>
          <td class="num" colspan="2">{fmtCost(m.costStart)} → {fmtCost(m.costTotal)}</td>
          <td></td>
        </tr>
        <tr>
          <td>Rows</td>
          <td class="num">{fmtRows(m.rowsExpected)}</td>
          <td class="num">{notExecuted ? '—' : ((analyzed && !isMaria && m.rowsActual > 0) ? fmtRows(m.rowsActual) : (isMaria ? '—' : (analyzed ? fmtRows(m.rowsActual) : '—')))}</td>
          <td>{(!notExecuted && hs.estimate) ? '⚠ estimate' : ''}</td>
        </tr>
        {#if analyzed && !isMaria}
          <tr>
            <td>Time (start → total)</td>
            <td class="num">—</td>
            <td class="num">{notExecuted ? '—' : fmtMs(m.timeStartMs)} → {notExecuted ? '—' : fmtMs(m.timeTotalMs)}</td>
            <td></td>
          </tr>
          <tr>
            <td>Loops</td>
            <td class="num">—</td>
            <td class="num">{notExecuted ? '—' : fmtRows(m.loops)}</td>
            <td>{(!notExecuted && hs.loops) ? '⚠ loops' : ''}</td>
          </tr>
        {/if}
        {#if !isMaria}
          <tr>
            <td>Buffers (hit / read / written)</td>
            <td class="num">—</td>
            <td class="num">{notExecuted ? '—' : fmtRows(m.buffersHit)} / {notExecuted ? '—' : fmtRows(m.buffersRead)} / {notExecuted ? '—' : fmtRows(m.buffersWritten)}</td>
            <td></td>
          </tr>
        {/if}
      </tbody>
    </table>

    {#if planWarnings && planWarnings.length > 0}
      <div class="node-detail__warn">
        <Pill tone="warning">{planWarnings.length} plan warning(s)</Pill>
      </div>
    {/if}

    {#if rawSlice}
      <details class="node-detail__raw">
        <summary>RAW node</summary>
        <pre class="code">{rawJson(rawSlice)}</pre>
      </details>
    {/if}
  </aside>
{/if}
