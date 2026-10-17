package cache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheBasic(t *testing.T) {
	c := New[string](2, time.Minute)
	if _, ok := c.Get("a"); ok {
		t.Fatal("empty cache should miss")
	}
	c.Set("a", "1")
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Fatalf("Get = %q, %v", v, ok)
	}
}

func TestCacheTTL(t *testing.T) {
	c := New[string](4, 15*time.Millisecond)
	c.Set("a", "1")
	time.Sleep(25 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Fatal("expired entry should miss")
	}
}

func TestCacheLRU(t *testing.T) {
	c := New[string](2, time.Minute)
	c.Set("a", "1")
	c.Set("b", "2")
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should exist")
	}
	c.Set("c", "3") // evicts least recently used (b)
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should survive")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c should exist")
	}
}

func TestCleanup(t *testing.T) {
	c := New[string](4, 10*time.Millisecond)
	c.Set("a", "1")
	c.Set("b", "2")
	time.Sleep(20 * time.Millisecond)
	if n := c.Cleanup(); n != 2 {
		t.Errorf("Cleanup removed %d, want 2", n)
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d, want 0", c.Len())
	}
}

func TestSingleflight(t *testing.T) {
	var g Group[int]
	var calls int32
	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := Do(&g, "same", func() (int, error) {
				atomic.AddInt32(&calls, 1)
				time.Sleep(10 * time.Millisecond)
				return 42, nil
			})
			if err != nil {
				t.Errorf("Do: %v", err)
			}
			results[i] = v
		}(i)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fn called %d times, want 1", got)
	}
	for _, v := range results {
		if v != 42 {
			t.Errorf("result = %d, want 42", v)
		}
	}
}

func TestCacheDisabled(t *testing.T) {
	c := New[string](0, time.Minute)
	c.Set("a", "1")
	if _, ok := c.Get("a"); ok {
		t.Fatal("disabled cache must always miss")
	}
}
