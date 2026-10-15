/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ghquota

import (
	"container/list"
	"sync"
)

// lruCache is an httpcache.Cache bounded by the bytes it holds: storing
// past maxBytes forgets the least recently used entries first. An entry
// bigger than the whole bound is not stored at all — it would only evict
// everything else and then itself.
type lruCache struct {
	maxBytes int

	mu      sync.Mutex
	bytes   int
	order   *list.List // front: most recently used
	entries map[string]*list.Element
}

type lruEntry struct {
	key   string
	value []byte
}

func newLRUCache(maxBytes int) *lruCache {
	return &lruCache{maxBytes: maxBytes, order: list.New(), entries: map[string]*list.Element{}}
}

func (c *lruCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry).value, true
}

func (c *lruCache) Set(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remove(key)
	if len(value) > c.maxBytes {
		return
	}
	c.entries[key] = c.order.PushFront(&lruEntry{key: key, value: value})
	c.bytes += len(value)
	for c.bytes > c.maxBytes {
		c.remove(c.order.Back().Value.(*lruEntry).key)
	}
}

func (c *lruCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remove(key)
}

func (c *lruCache) remove(key string) {
	el, ok := c.entries[key]
	if !ok {
		return
	}
	c.order.Remove(el)
	delete(c.entries, key)
	c.bytes -= len(el.Value.(*lruEntry).value)
}
