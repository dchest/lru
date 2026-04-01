// Copyright 2013-2026 Dmitry Chestnykh. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package lru implements Least Recently Used cache algorithm.
//
// Cache capacity can be optionally limited by both size in bytes and a number
// of items. Items can keep track of their modification time.
// It is possible to set eviction callback, which is called when an
// item is removed from cache.
//
// Example:
//
//	// Create a 1 MB cache.
//	c := lru.New[string, []byte](0).WithMaxBytes(1024*1024)
//	...
//	// Query cache and insert item if it's not there.
//	var x []byte
//	v, ok := c.Get("my_key")
//	if !ok {
//		// Item is not in cache, fetch it from main storage...
//		v = ...
//		// ...then add it to cache.
//		c.SetBytes("my_key", v, int64(cap(v)))
//	}
//	// Use v.
package lru

import (
	"math"
	"sync"
	"time"
)

type Cache[Key comparable, Value any] struct {
	mu sync.RWMutex

	m    map[Key]*item[Key, Value] // map of keys to list items
	root *item[Key, Value]         // sentinel list item

	len      int   // number of items in the cache
	bytes    int64 // byte size of all items in the cache if added via SetBytes
	maxItems int   // maximum number of items the cache can contain (0 = unlimited)
	maxBytes int64 // maximum byte capacity of cache (0 = unlimited)

	// Item expiration duration.
	//
	// An item is removed from cache when trying to get it
	// if the given time passed since its modification time.
	//
	// Set to zero for no expiration (default).
	expires time.Duration

	onEvict func(key Key, value Value)
}

// item represents a cached item with additional information.
type item[Key comparable, Value any] struct {
	next *item[Key, Value]
	prev *item[Key, Value]

	Key     Key
	Value   Value
	Size    int64     // byte size of value
	ModTime time.Time // when this item was added to cache
}

// New returns a cache instance with the given maximum number of items.
// If maxItems is zero, the cache size is unlimited.
func New[Key comparable, Value any](maxItems int) *Cache[Key, Value] {
	root := &item[Key, Value]{}
	root.next = root
	root.prev = root
	return &Cache[Key, Value]{
		m:        make(map[Key]*item[Key, Value], maxItems),
		root:     root,
		maxItems: maxItems,
	}
}

// WithMaxBytes returns a cache with the given maximum byte capacity.
// If maxBytes is zero, the cache byte capacity is unlimited.
//
// WithMaxBytes must be called immediately after New, before any items are
// added to cache, otherwise the behavior is undefined.
func (c *Cache[Key, Value]) WithMaxBytes(maxBytes int64) *Cache[Key, Value] {
	c.maxBytes = maxBytes
	return c
}

// WithExpiration returns a cache with the given item expiration duration.
//
// An item is removed from cache when trying to get it if the given
// time passed since its modification time.
//
// WithExpiration must be called immediately after New, before any items are
// added to cache, otherwise the behavior is undefined.
func (c *Cache[Key, Value]) WithExpiration(expires time.Duration) *Cache[Key, Value] {
	c.expires = expires
	return c
}

// WithEvict returns a cache with the given eviction handler.
//
// Eviction handler is called when an item is removed from cache, either by
// eviction or by Remove method. It is called with the key and value of the
// removed item.
//
// WithEvict must be called immediately after New, before any items are
// added to cache, otherwise the behavior is undefined.
func (c *Cache[Key, Value]) WithEvict(fn func(key Key, value Value)) *Cache[Key, Value] {
	c.onEvict = fn
	return c
}

// Reset clears the cache.
//
// If eviction handler is set, it is called for each item of the cache.
func (c *Cache[Key, Value]) Reset() {
	c.mu.Lock()
	evictedItems := make([]item[Key, Value], 0, c.len)
	if c.onEvict != nil {
		for elem := c.root.next; elem != c.root; elem = elem.next {
			evictedItems = append(evictedItems, *elem)
		}
	}
	c.m = make(map[Key]*item[Key, Value], c.maxItems)
	c.root.next = c.root
	c.root.prev = c.root
	c.bytes = 0
	c.len = 0
	c.mu.Unlock()
	if c.onEvict != nil {
		for _, it := range evictedItems {
			c.onEvict(it.Key, it.Value)
		}
	}
}

// removeFromList removes a given item from the linked list.
// Cache must be locked.
func (c *Cache[Key, Value]) removeFromList(it *item[Key, Value]) {
	it.prev.next = it.next
	it.next.prev = it.prev
	it.next = nil
	it.prev = nil
	c.len -= 1
}

// pushFront pushes a given item to the front of the linked list.
// Cache must be locked.
func (c *Cache[Key, Value]) pushFront(it *item[Key, Value]) {
	it.prev = c.root
	it.next = c.root.next
	it.prev.next = it
	it.next.prev = it
	c.len += 1
}

// moveToFront moves a given item to the front of the linked list.
// Cache must be locked.
func (c *Cache[Key, Value]) moveToFront(it *item[Key, Value]) {
	if c.root.next == it {
		// Already at front, no need to move.
		return
	}
	// Remove from current position.
	it.prev.next = it.next
	it.next.prev = it.prev
	// Push to front.
	it.prev = c.root
	it.next = c.root.next
	it.prev.next = it
	it.next.prev = it
}

// SetBytes sets or updates a cache item for the given key to the given value,
// and the given value size in bytes.
//
// It panics if the given size is negative or if the new size causes
// cache size to overflow int64, which shouldn't happen in practice,
// as allocating that much memory is impossible.
func (c *Cache[Key, Value]) SetBytes(key Key, value Value, size int64) {
	c.mu.Lock()
	evictedItems := make([]item[Key, Value], 0, 1)

	it, ok := c.m[key]
	if ok {
		// Element exists, push it to front,
		// and then update value etc.
		c.bytes -= it.Size
		c.moveToFront(it)
	} else {
		// Check if we can evict an item to reuse.
		if c.maxItems > 0 && c.len >= c.maxItems {
			it = c.dropTail()
		}
		if it == nil && c.maxBytes > 0 && c.bytes+size > c.maxBytes {
			it = c.dropTail()
		}
		if it != nil {
			// Reuse dropped element.
			evictedItems = append(evictedItems, *it)
			it.Key = key
		} else {
			// Allocate a new one
			it = &item[Key, Value]{Key: key}
		}
		c.pushFront(it)
		c.m[key] = it
	}
	// Set or update item content.
	it.Value = value
	it.Size = size
	if it.Size < 0 {
		c.mu.Unlock()
		panic("cache: value has negative size")
	}
	if c.expires != 0 {
		it.ModTime = time.Now()
	}

	// Update cache size.
	if it.Size >= math.MaxInt64-c.bytes {
		c.mu.Unlock()
		panic("cache: value size is too big")
	}
	c.bytes += it.Size

	// Enforce capacity.
	// Check maxItems.
	if c.maxItems > 0 {
		for c.len > c.maxItems {
			if it := c.dropTail(); it != nil {
				evictedItems = append(evictedItems, *it)
			} else {
				break
			}
		}
	}
	// Check maxBytes.
	if c.maxBytes > 0 {
		for c.bytes > c.maxBytes {
			if it := c.dropTail(); it != nil {
				evictedItems = append(evictedItems, *it)
			} else {
				break
			}
		}
	}

	c.mu.Unlock()

	if c.onEvict != nil {
		for _, it := range evictedItems {
			c.onEvict(it.Key, it.Value)
		}
	}
}

// Set sets or updates a cache item for the given key to the given value.
//
// The value size is set to zero, so it doesn't affect cache byte capacity.
// If configured via WithMaxBytes, use SetBytes instead, which allows
// specifying the value size in bytes.
func (c *Cache[Key, Value]) Set(key Key, value Value) {
	c.SetBytes(key, value, 0)
}

// GetModTime returns a value and modification time of the item cached under
// the given key. The modification time is non-zero only if cache was
// configured with expiration time via WithExpiration.
func (c *Cache[Key, Value]) GetModTime(key Key) (value Value, modTime time.Time, ok bool) {
	c.mu.Lock()
	it, ok := c.m[key]
	if !ok {
		c.mu.Unlock()
		return value, modTime, false
	}
	// Check for expiration.
	if c.expires != 0 && time.Since(it.ModTime) > c.expires {
		// Item expired, delete it.
		c.removeElement(it)
		c.mu.Unlock()
		if c.onEvict != nil {
			c.onEvict(it.Key, it.Value)
		}
		return value, modTime, false
	}
	c.moveToFront(it)
	value, modTime = it.Value, it.ModTime
	c.mu.Unlock()
	return value, modTime, true
}

// Get returns a value of item cached under the given key.
// If there is no such key in the cache, it returns zero value and false.
func (c *Cache[Key, Value]) Get(key Key) (value Value, ok bool) {
	value, _, ok = c.GetModTime(key)
	return
}

// Oldest returns a copy of least recently used item. If cache contains no
// items, the second return value is false. Accessing the oldest item via this
// function doesn't change the order of items in cache.
func (c *Cache[Key, Value]) Oldest() (key Key, value Value, modTime time.Time, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if tail := c.root.prev; tail != c.root {
		return tail.Key, tail.Value, tail.ModTime, true
	}
	return
}

// removeElement removes a given list element from cache
// and calls onEvict if it's present.
// Cache must be locked.
func (c *Cache[Key, Value]) removeElement(it *item[Key, Value]) {
	c.removeFromList(it)
	delete(c.m, it.Key)
	// Update cached byte size.
	c.bytes -= it.Size
}

// dropTail drops the least accessed item from cache.
// Cache must be locked.
func (c *Cache[Key, Value]) dropTail() *item[Key, Value] {
	tail := c.root.prev
	if tail == c.root {
		return nil
	}
	c.removeElement(tail)
	return tail
}

// Remove deletes an item with the given key from the cache.
func (c *Cache[Key, Value]) Remove(key Key) bool {
	c.mu.Lock()
	elem, ok := c.m[key]
	if !ok {
		c.mu.Unlock()
		return false
	}
	c.removeElement(elem)
	c.mu.Unlock()
	if c.onEvict != nil {
		c.onEvict(elem.Key, elem.Value)
	}
	return true
}

// Len returns the number of items in the cache.
func (c *Cache[Key, Value]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.len
}

// Size returns the size of all values in the cache,
// as recorded by SetBytes.
func (c *Cache[Key, Value]) Size() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bytes
}

// Keys returns a slice of all keys in the cache.
func (c *Cache[Key, Value]) Keys() []Key {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]Key, 0, c.len)
	for elem := c.root.next; elem != c.root; elem = elem.next {
		keys = append(keys, elem.Key)
	}
	return keys
}
