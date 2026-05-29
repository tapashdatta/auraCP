import { describe, it, expect, beforeEach, vi } from 'vitest'
import { register, complete, cancel, cancelAll, isExecuting, activeCount, onCompletion, _resetForTests } from './execRegistry.svelte.js'

function fakeHandle() {
  return { ref: 'r', cancel: vi.fn() }
}

describe('execRegistry', () => {
  beforeEach(() => { _resetForTests() })

  it('register stores handle and isExecuting reflects it', () => {
    const h = fakeHandle()
    register('tab1', h)
    expect(isExecuting('tab1')).toBe(true)
    expect(activeCount()).toBe(1)
  })

  it('register cancels prior handle on same tab', () => {
    const a = fakeHandle()
    const b = fakeHandle()
    register('tab1', a)
    register('tab1', b)
    expect(a.cancel).toHaveBeenCalled()
    expect(b.cancel).not.toHaveBeenCalled()
  })

  it('EXEC-3: prior handle is cancelled BEFORE the new one replaces it', () => {
    // Regression: a rapid double-Execute on the same tabId must cancel
    // the in-flight exec first so two queries don't race the server.
    const events = []
    const a = { ref: 'a', cancel: () => events.push('cancel:a') }
    const b = { ref: 'b', cancel: () => events.push('cancel:b') }
    register('tab1', a)
    events.push('register:a-done')
    register('tab1', b) // should fire cancel:a before storing b
    expect(events).toEqual(['register:a-done', 'cancel:a'])
    expect(isExecuting('tab1')).toBe(true) // b is stored
  })

  it('cancel invokes cancel() and removes handle', () => {
    const a = fakeHandle()
    register('tab1', a)
    expect(cancel('tab1')).toBe(true)
    expect(a.cancel).toHaveBeenCalled()
    expect(isExecuting('tab1')).toBe(false)
  })

  it('cancelAll clears every handle and calls cancel', () => {
    const a = fakeHandle(); const b = fakeHandle()
    register('tab1', a); register('tab2', b)
    cancelAll()
    expect(a.cancel).toHaveBeenCalled()
    expect(b.cancel).toHaveBeenCalled()
    expect(activeCount()).toBe(0)
  })

  it('complete fires completion listeners', () => {
    const seen = []
    const off = onCompletion((tid) => seen.push(tid))
    const a = fakeHandle()
    register('tab1', a)
    complete('tab1')
    expect(seen).toEqual(['tab1'])
    expect(isExecuting('tab1')).toBe(false)
    off()
  })
})
