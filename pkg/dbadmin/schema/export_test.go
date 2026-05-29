package schema

// CacheSizes returns (entries, lruOrder) sizes for tests verifying
// that the internal LRU bookkeeping doesn't leak duplicates and that
// Invalidate drops every map entry.
func CacheSizes(c *Cache) (int, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries), len(c.lruOrder)
}
