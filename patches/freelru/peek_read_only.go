// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package freelru

// PeekReadOnly treats expired entries as absent without evicting them or
// changing recency, lifetime or metrics.
func (lru *Cache[K, V]) PeekReadOnly(key K) (V, bool) {
	if sharded := lru.sharded; sharded != nil {
		hash := sharded.hash(key)
		shard := (hash >> 16) & sharded.mask
		sharded.mus[shard].Lock()
		defer sharded.mus[shard].Unlock()
		return sharded.lrus[shard].peekReadOnly(hash, key)
	}
	synced := lru.synced
	hash := synced.lru.hash(key)
	synced.mu.Lock()
	defer synced.mu.Unlock()
	return synced.lru.peekReadOnly(hash, key)
}

func (lru *LRU[K, V]) peekReadOnly(hash uint32, key K) (value V, ok bool) {
	_, start := lru.hashToPos(hash)
	if start == emptyBucket {
		return value, false
	}
	for pos := start; ; {
		entry := &lru.elements[pos]
		if entry.key == key {
			if entry.expire != 0 && entry.expire <= now() {
				return value, false
			}
			if lru.healthCheck != nil && !lru.healthCheck(key, entry.value) {
				return value, false
			}
			return entry.value, true
		}
		pos = entry.nextBucket
		if pos == start {
			return value, false
		}
	}
}
