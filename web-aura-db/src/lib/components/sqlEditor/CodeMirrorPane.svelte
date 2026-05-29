<script>
  import { onMount, onDestroy } from 'svelte'
  import { createEditorView, replaceDoc } from '../../sqlEditor/editor.js'

  /** @type {{
   *   doc?: string,
   *   engine: 'mariadb'|'postgres',
   *   connId: string,
   *   onChange?: (doc:string)=>void,
   *   onCursor?: (pos:number)=>void,
   *   onExecute?: (view:any, pos:number)=>void,
   *   onExecuteAll?: (view:any)=>void,
   *   onExplain?: (view:any, pos:number)=>void,
   *   onCancel?: ()=>void,
   *   onFormat?: ()=>void,
   *   onSave?: ()=>void,
   * }} */
  let { doc = '', engine, connId, onChange, onCursor, onExecute, onExecuteAll, onExplain, onCancel, onFormat, onSave } = $props()

  /** @type {HTMLDivElement|undefined} */
  let host = $state(undefined)
  /** @type {any} */
  let view = null

  onMount(() => {
    if (!host) return
    view = createEditorView({
      parent: host,
      doc,
      engine,
      connId,
      onChange,
      onCursor,
      onExecute,
      onExecuteAll,
      onExplain,
      onCancel,
      onFormat,
      onSave,
    })
  })

  onDestroy(() => {
    try { view?.destroy() } catch { /* ignore */ }
    view = null
  })

  /**
   * Replace the doc atomically (caller drives via bind:this).
   * @param {string} next
   */
  export function setDoc(next) {
    if (!view) return
    replaceDoc(view, next)
  }

  /** Return the current doc string. */
  export function getDoc() {
    return view ? view.state.doc.toString() : doc
  }

  /** Return the current cursor offset. */
  export function getCursor() {
    return view ? view.state.selection.main.head : 0
  }

  /** Focus the editor. */
  export function focus() {
    view?.focus()
  }
</script>

<div bind:this={host} class="cm-pane" data-testid="cm-pane"></div>
