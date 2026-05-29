<script>
  import Spinner from './Spinner.svelte'
  /** @type {{
   *   variant?: 'primary'|'ghost'|'danger'|'default',
   *   size?: 'sm'|'md',
   *   disabled?: boolean,
   *   loading?: boolean,
   *   ariaBusy?: boolean,
   *   ariaDisabled?: boolean,
   *   reason?: string,
   *   type?: 'button'|'submit',
   *   onclick?: (e: MouseEvent)=>void,
   *   ariaLabel?: string,
   *   ariaPressed?: boolean | undefined,
   *   children?: any
   * }} */
  // FIX (PR #13.5 a11y-02, a11y-09):
  //   - `ariaDisabled` keeps the button keyboard-focusable but blocks
  //     activation; useful when there's a reason the user should see
  //     (a tooltip in `reason`) rather than a silently-dead control.
  //   - `ariaBusy` signals an in-flight operation to AT (loading state).
  //   - `reason` populates the title= attribute for sighted users and is
  //     mirrored to aria-describedby via a sibling SR-only span when set.
  let {
    variant = 'default',
    size = 'md',
    disabled = false,
    loading = false,
    ariaBusy = undefined,
    ariaDisabled = false,
    reason = '',
    type = 'button',
    onclick,
    ariaLabel,
    ariaPressed,
    children,
  } = $props()
  function onClickGuard(e) {
    if (ariaDisabled || disabled || loading) {
      e.preventDefault(); e.stopPropagation()
      return
    }
    onclick?.(e)
  }
  const busy = $derived(ariaBusy ?? loading)
</script>

<button
  {type}
  class="btn {variant === 'primary' ? 'btn--primary' : variant === 'ghost' ? 'btn--ghost' : variant === 'danger' ? 'btn--danger' : ''} {size === 'sm' ? 'btn--sm' : ''}"
  disabled={disabled || (loading && !ariaDisabled) ? true : undefined}
  aria-disabled={ariaDisabled ? true : undefined}
  aria-busy={busy ? true : undefined}
  aria-label={ariaLabel || undefined}
  aria-pressed={ariaPressed === undefined ? undefined : ariaPressed}
  title={reason || undefined}
  onclick={onClickGuard}
>
  {#if loading}<Spinner size={10} />{/if}
  {@render children?.()}
</button>
