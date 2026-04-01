// Copyright 2013-2015 Dmitry Chestnykh. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lru

import (
	"strconv"
	"testing"
	"time"
)

func getIntCheck(t *testing.T, c *Cache[string, int], key string, expectedValue int) {
	v, ok := c.Get(key)
	if !ok {
		t.Fatalf("cache doesn't contain %q", key)
	}
	if v != expectedValue {
		t.Fatalf("wrong stored value: expected %d, got %d", expectedValue, v)
	}
}

func setInt(c *Cache[string, int], key string, value int) {
	c.Set(key, value)
}

func TestMaxItems(t *testing.T) {
	c := New[string, int](3)
	setInt(c, "one", 1)
	setInt(c, "two", 2)
	setInt(c, "three", 3)

	// Try getting every, making sure we get "two" first,
	// so that it will later be pushed off the cache.

	getIntCheck(t, c, "two", 2)
	getIntCheck(t, c, "one", 1)
	getIntCheck(t, c, "three", 3)

	// Add new value, causing cache to drop "two".
	setInt(c, "four", 4)

	// Check that it was dropped...
	if v, ok := c.Get("two"); ok {
		t.Fatalf("cache didn't drop \"two\" (got %v)", v)
	}

	// ...and other elements are still there.
	getIntCheck(t, c, "one", 1)
	getIntCheck(t, c, "three", 3)
	getIntCheck(t, c, "four", 4)

	// Check that "one" is the oldest item.
	key, _, _, _ := c.Oldest()
	if key != "one" {
		t.Fatalf("oldest item is %s, expected %s", key, "one")
	}

	// Replace element's value.
	setInt(c, "four", 100)
	getIntCheck(t, c, "four", 100)
}

func TestMaxBytes(t *testing.T) {
	removeCalled := 0
	c := New[string, []byte](0).
		WithMaxBytes(1000).
		WithEvict(func(key string, value []byte) { removeCalled++ })
	b := make([]byte, 100)
	// Add 1100 bytes.
	for i := 0; i < 11; i++ {
		c.SetBytes(strconv.Itoa(i), b, int64(cap(b)))
	}
	// Ensure there's no 0th item, as it should be dropped.
	if _, ok := c.Get("0"); ok {
		t.Fatalf("cache didn't drop 0th item")
	}
	// Ensure items 1-10 exist.
	for i := 1; i < 11; i++ {
		if _, ok := c.Get(strconv.Itoa(i)); !ok {
			t.Fatalf("cache item %d doesn't exist", i)
		}
	}
	if removeCalled != 1 {
		t.Fatalf("removeHandler was called %d times, expected %d", removeCalled, 1)
	}
}

func TestExpiration(t *testing.T) {
	c := New[string, string](0).WithExpiration(1 * time.Millisecond)
	c.Set("hello", "world")
	time.Sleep(2 * time.Millisecond)
	_, ok := c.Get("hello")
	if ok {
		t.Fatalf("didn't remove expired item")
	}
}

func BenchmarkSet(b *testing.B) {
	c := New[int, []byte](1000)
	bs := make([]byte, 100)
	for i := 0; b.Loop(); i++ {
		c.Set(i, bs)
	}
}

func BenchmarkSetBytes(b *testing.B) {
	c := New[int, []byte](1000).WithMaxBytes(100 * 1000)
	bs := make([]byte, 100)
	for i := 0; b.Loop(); i++ {
		c.SetBytes(i, bs, int64(len(bs)))
	}
}

func BenchmarkGet(b *testing.B) {
	c := New[string, []byte](1000)
	c.SetBytes("test", make([]byte, 100), 100)
	for b.Loop() {
		c.Get("test")
	}
}
